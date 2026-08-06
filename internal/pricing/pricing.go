// Package pricing keeps a local copy of LiteLLM's model price database, the
// de-facto community source for per-token rates across providers.
//
// We need it because only one engine values its own runs: claude reports
// total_cost_usd, codex reports nothing at all. Deriving codex's spend means
// pricing its tokens ourselves, and that cannot be done with a blended rate —
// a cached read costs roughly a tenth of fresh input and a sixtieth of output,
// so the same token count spans a 6x range depending on the split. The classes
// history records map onto the classes this table prices.
//
// The copy lives in the app's cache dir rather than its data dir on purpose:
// it is re-fetchable, so losing it costs a download rather than a record. It
// is also never bundled into the binary, so nothing here redistributes
// LiteLLM's file.
package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SourceURL is the raw file on LiteLLM's default branch. Polled with a
// conditional GET rather than re-downloaded: the endpoint serves an ETag, so
// an unchanged database answers 304 with an empty body instead of 1.6MB.
const SourceURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// RefreshInterval is how often the daemon re-checks. Prices move on the order
// of months, so this is about not going stale rather than staying current to
// the minute, and an unchanged check costs one 304.
const RefreshInterval = 6 * time.Hour

const (
	tableFile = "model-prices.json"
	metaFile  = "model-prices.meta.json"
)

// Rates is one model's per-token prices, in USD. Zero means the database
// listed no price for that class, which for cache classes is common and
// normal: an engine with no explicit cache write has nothing to price.
type Rates struct {
	Input      float64 `json:"input_cost_per_token"`
	Output     float64 `json:"output_cost_per_token"`
	CacheWrite float64 `json:"cache_creation_input_token_cost"`
	// CacheWrite1h is the longer time-to-live cache write, billed above the
	// 5-minute one. claude reports the two tiers apart; we do not model which
	// tier a run used, so this is available for callers that read usage_raw.
	CacheWrite1h float64 `json:"cache_creation_input_token_cost_above_1hr"`
	CacheRead    float64 `json:"cache_read_input_token_cost"`
}

// Priced says whether the entry carries enough to value a run at all. A model
// present in the database but with no input or output price prices nothing.
func (r Rates) Priced() bool { return r.Input > 0 || r.Output > 0 }

// EffectiveCacheWrite is the rate a cache write is actually valued at: the
// database's cache-write price, falling back to the input rate when it prices
// no cache-write class, which is the closest honest answer since the model
// processed that content either way.
//
// Exported because the rule has to hold on BOTH paths that value a review. It
// used to live inside Cost, so the live path applied it and the store's
// backfill SQL (which is handed the raw rate fields) did not: the same review
// was worth two different amounts depending on which one priced it, and every
// backfilled cache-write-heavy row was silently under-valued.
func (r Rates) EffectiveCacheWrite() float64 {
	if r.CacheWrite == 0 {
		return r.Input
	}
	return r.CacheWrite
}

// Cost values one run's token classes.
func (r Rates) Cost(input, output, cacheWrite, cacheRead int) float64 {
	write := r.EffectiveCacheWrite()
	return float64(input)*r.Input +
		float64(output)*r.Output +
		float64(cacheWrite)*write +
		float64(cacheRead)*r.CacheRead
}

// meta is what we know about the cached copy, kept beside it so a refresh can
// be conditional and a reader can tell how stale the answer is.
type meta struct {
	ETag      string    `json:"etag"`
	FetchedAt time.Time `json:"fetched_at"`
	Source    string    `json:"source_url"`
	Models    int       `json:"models"`
}

// Cache is the local copy: a read-through table plus the metadata needed to
// re-check cheaply. Safe for concurrent use — the dashboard reads it while the
// daemon's refresh loop writes.
type Cache struct {
	dir    string
	client *http.Client

	mu    sync.RWMutex
	rates map[string]Rates
	meta  meta
}

// Open reads whatever is already cached. It never touches the network, so a
// daemon boots at the same speed offline as on; a cache that is missing or
// unreadable yields an empty table rather than an error, because absent
// pricing must degrade to "no estimate" and never block a review.
func Open(dir string) *Cache {
	c := &Cache{dir: dir, client: &http.Client{Timeout: 30 * time.Second}}
	c.loadFromDisk()
	return c
}

func (c *Cache) loadFromDisk() {
	raw, err := os.ReadFile(filepath.Join(c.dir, tableFile))
	if err != nil {
		return
	}
	rates, err := parseTable(raw)
	if err != nil {
		return
	}
	var m meta
	if metaRaw, err := os.ReadFile(filepath.Join(c.dir, metaFile)); err == nil {
		_ = json.Unmarshal(metaRaw, &m)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rates, c.meta = rates, m
}

// parseTable reads LiteLLM's shape: a flat object of model id -> entry, with
// a "sample_spec" key that documents the schema rather than naming a model.
// Entries carry far more than prices (context windows, capability flags);
// everything unrecognised is ignored, so the file growing cannot break this.
func parseTable(raw []byte) (map[string]Rates, error) {
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("parse price table: %w", err)
	}
	rates := make(map[string]Rates, len(entries))
	for model, entry := range entries {
		if model == "sample_spec" {
			continue
		}
		var r Rates
		if err := json.Unmarshal(entry, &r); err != nil {
			continue // an entry we can't read is one model unpriced, not a failure
		}
		rates[model] = r
	}
	if len(rates) == 0 {
		return nil, errors.New("price table held no models")
	}
	return rates, nil
}

// Lookup returns a model's rates. The second result is false for a model the
// database doesn't list, which callers must treat as "cannot estimate" rather
// than as free.
func (c *Cache) Lookup(model string) (Rates, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.rates[model]
	if !ok || !r.Priced() {
		return Rates{}, false
	}
	return r, true
}

// Status describes the cached copy for the doctor check and the dashboard.
type Status struct {
	Models    int       `json:"models"`
	FetchedAt time.Time `json:"fetched_at,omitempty"`
	Source    string    `json:"source_url,omitempty"`
}

func (c *Cache) Status() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return Status{Models: len(c.rates), FetchedAt: c.meta.FetchedAt, Source: SourceURL}
}

// Stale says whether the copy is old enough to re-check. A cache that was
// never fetched is always stale.
func (c *Cache) Stale(now time.Time) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return now.Sub(c.meta.FetchedAt) >= RefreshInterval
}

// Refresh re-checks the source, conditional on the stored ETag. It reports
// whether the table actually changed, so a caller can log a no-op refresh
// differently from a real update.
//
// A 304 still stamps FetchedAt: the copy was confirmed current, which is what
// the staleness clock is really tracking.
func (c *Cache) Refresh(ctx context.Context, now time.Time) (bool, error) {
	c.mu.RLock()
	etag := c.meta.ETag
	c.mu.RUnlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, SourceURL, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", "agent-code-review")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("fetch price table: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotModified {
		return false, c.stampChecked(now)
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("fetch price table: %s", resp.Status)
	}
	raw, err := readAll(resp)
	if err != nil {
		return false, err
	}
	// Parse BEFORE writing: a truncated or reshaped download must leave the
	// previous good copy in place rather than replacing it with something
	// unreadable.
	rates, err := parseTable(raw)
	if err != nil {
		return false, err
	}
	m := meta{ETag: resp.Header.Get("ETag"), FetchedAt: now, Source: SourceURL, Models: len(rates)}
	if err := c.write(raw, m); err != nil {
		return false, err
	}
	c.mu.Lock()
	c.rates, c.meta = rates, m
	c.mu.Unlock()
	return true, nil
}

func (c *Cache) stampChecked(now time.Time) error {
	c.mu.Lock()
	c.meta.FetchedAt = now
	m := c.meta
	c.mu.Unlock()
	return writeJSON(filepath.Join(c.dir, metaFile), m)
}

func (c *Cache) write(table []byte, m meta) error {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(c.dir, tableFile), table); err != nil {
		return err
	}
	return writeJSON(filepath.Join(c.dir, metaFile), m)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'))
}

// writeAtomic swaps the file in by rename, so a reader never sees a
// half-written table and a crash mid-write leaves the previous copy intact.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// readAll reads the response body with a ceiling, so a source that starts
// answering with something enormous cannot exhaust memory. The real file is
// ~1.6MB; the cap is generous enough to absorb years of growth.
func readAll(resp *http.Response) ([]byte, error) {
	const maxBytes = 32 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read price table: %w", err)
	}
	if len(raw) > maxBytes {
		return nil, fmt.Errorf("price table exceeds %d bytes", maxBytes)
	}
	return raw, nil
}

// Poll keeps the copy current for as long as ctx lives, refreshing on the
// interval and once at start. Mirrors usage.Cache.Poll: a background loop the
// daemon owns, so nothing on a request path ever waits on the network.
//
// afterCheck runs after every check, refreshed or not, so a caller can settle
// anything derived from the rates (backfilling valuations for rows completed
// while the table was unreachable) without running a second loop on its own
// schedule. Optional.
//
// Refresh failures are logged and retried on the next tick rather than
// returned. Pricing is an enrichment; a network that is down must cost an
// estimate, never a review.
func (c *Cache) Poll(ctx context.Context, logf func(string, ...any), afterCheck func()) {
	ticker := time.NewTicker(RefreshInterval)
	defer ticker.Stop()
	for {
		if !c.Stale(time.Now()) {
			// A copy another process already refreshed needs no request; the
			// interval is about freshness, not about ticking on schedule.
			logf("pricing: %d models, checked %s", c.Status().Models, c.Status().FetchedAt.Format(time.RFC3339))
		} else if updated, err := c.Refresh(ctx, time.Now()); err != nil {
			logf("pricing: refresh failed: %v (estimates use the last good copy)", err)
		} else if updated {
			logf("pricing: updated, %d models from %s", c.Status().Models, SourceURL)
		} else {
			logf("pricing: unchanged, %d models", c.Status().Models)
		}
		if afterCheck != nil {
			afterCheck()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
