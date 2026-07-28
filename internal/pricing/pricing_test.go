package pricing

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A trimmed slice of the real file, including the sample_spec key LiteLLM
// uses to document its own schema and an entry with no prices at all.
const sampleTable = `{
  "sample_spec": {"input_cost_per_token": 0.0, "note": "this is a schema doc, not a model"},
  "gpt-5.6-terra": {
    "input_cost_per_token": 2.5e-06,
    "output_cost_per_token": 1.5e-05,
    "cache_read_input_token_cost": 2.5e-07,
    "cache_creation_input_token_cost": 3.125e-06,
    "max_input_tokens": 272000
  },
  "claude-opus-5": {
    "input_cost_per_token": 5e-06,
    "output_cost_per_token": 2.5e-05,
    "cache_creation_input_token_cost": 6.25e-06,
    "cache_creation_input_token_cost_above_1hr": 1e-05,
    "cache_read_input_token_cost": 5e-07
  },
  "some-embedding-model": {"mode": "embedding"}
}`

func serve(t *testing.T, body string, etag string, hits *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			*hits++
		}
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// withSource points the package at a test server. SourceURL is a const so the
// request URL is built from it; the test swaps the client's transport instead,
// which also proves the conditional-GET headers are what we claim.
func withSource(t *testing.T, c *Cache, srv *httptest.Server) {
	t.Helper()
	base := srv.URL
	c.client = &http.Client{Transport: rewriteTo(base)}
}

type rewriteTo string

func (r rewriteTo) RoundTrip(req *http.Request) (*http.Response, error) {
	target := *req.URL
	base, err := http.NewRequest(req.Method, string(r), nil)
	if err != nil {
		return nil, err
	}
	target.Scheme, target.Host = base.URL.Scheme, base.URL.Host
	clone := req.Clone(req.Context())
	clone.URL = &target
	return http.DefaultTransport.RoundTrip(clone)
}

func TestParseTableSkipsTheSchemaDoc(t *testing.T) {
	rates, err := parseTable([]byte(sampleTable))
	if err != nil {
		t.Fatal(err)
	}
	// sample_spec is documentation, not a model: pricing a run against it
	// would value every token at zero.
	if _, ok := rates["sample_spec"]; ok {
		t.Error("sample_spec must not be treated as a model")
	}
	if got := rates["gpt-5.6-terra"].Input; got != 2.5e-06 {
		t.Errorf("input rate = %v", got)
	}
	if got := rates["claude-opus-5"].CacheWrite1h; got != 1e-05 {
		t.Errorf("1h cache-write tier = %v, want it kept apart from the 5m one", got)
	}
}

// A model in the table with no input or output price cannot value anything.
// Returning it would price a run at $0, which reads as "free" rather than
// "unknown" everywhere downstream.
func TestLookupRejectsUnpricedModels(t *testing.T) {
	c := &Cache{dir: t.TempDir()}
	rates, err := parseTable([]byte(sampleTable))
	if err != nil {
		t.Fatal(err)
	}
	c.rates = rates

	if _, ok := c.Lookup("some-embedding-model"); ok {
		t.Error("a model with no token prices must not report as priced")
	}
	if _, ok := c.Lookup("model-that-does-not-exist"); ok {
		t.Error("an absent model must not report as priced")
	}
	if _, ok := c.Lookup("gpt-5.6-terra"); !ok {
		t.Error("a priced model must be found")
	}
}

// The whole reason for keeping token classes apart: pricing them as one number
// is wrong by multiples. Same 100k tokens, three different splits.
func TestCostPricesClassesApart(t *testing.T) {
	rates, _ := parseTable([]byte(sampleTable))
	r := rates["gpt-5.6-terra"]

	allInput := r.Cost(100_000, 0, 0, 0)
	allOutput := r.Cost(0, 100_000, 0, 0)
	allCached := r.Cost(0, 0, 0, 100_000)

	near := func(got, want float64) bool { return math.Abs(got-want) < 1e-9 }
	if !near(allInput, 0.25) {
		t.Errorf("100k input = %v, want 0.25", allInput)
	}
	if !near(allOutput, 1.5) {
		t.Errorf("100k output = %v, want 1.50", allOutput)
	}
	if !near(allCached, 0.025) {
		t.Errorf("100k cached reads = %v, want 0.025", allCached)
	}
	// 60x between the cheapest and dearest class is the reason a blended
	// figure cannot be priced.
	if !near(allOutput/allCached, 60) {
		t.Errorf("output/cached ratio = %v, want 60", allOutput/allCached)
	}
}

// A model whose cache-write class is unpriced still has to value those tokens:
// the model processed that content, so the input rate is the closest honest
// answer, and charging 0 would understate every claude run.
func TestCostFallsBackToInputForUnpricedCacheWrites(t *testing.T) {
	r := Rates{Input: 2e-06, Output: 1e-05}
	if got := r.Cost(0, 0, 1_000_000, 0); math.Abs(got-2.0) > 1e-9 {
		t.Errorf("cache writes = %v, want them priced at the input rate (2.0)", got)
	}
}

func TestRefreshWritesThenServesFromDisk(t *testing.T) {
	dir := t.TempDir()
	hits := 0
	srv := serve(t, sampleTable, `"v1"`, &hits)

	c := Open(dir)
	withSource(t, c, srv)
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	updated, err := c.Refresh(context.Background(), now)
	if err != nil || !updated {
		t.Fatalf("first refresh: updated=%v err=%v", updated, err)
	}
	if _, err := os.Stat(filepath.Join(dir, tableFile)); err != nil {
		t.Errorf("table not written: %v", err)
	}

	// A second process reads the cache without any network at all.
	reopened := Open(dir)
	if _, ok := reopened.Lookup("claude-opus-5"); !ok {
		t.Error("reopened cache must serve the model from disk")
	}
	if got := reopened.Status().FetchedAt; !got.Equal(now) {
		t.Errorf("FetchedAt = %v, want it persisted as %v", got, now)
	}
}

// The point of the ETag: an unchanged database must cost a 304, not 1.6MB.
func TestRefreshIsConditionalOnTheETag(t *testing.T) {
	dir := t.TempDir()
	srv := serve(t, sampleTable, `"v1"`, nil)
	c := Open(dir)
	withSource(t, c, srv)

	first := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	if updated, err := c.Refresh(context.Background(), first); err != nil || !updated {
		t.Fatalf("first refresh: updated=%v err=%v", updated, err)
	}

	later := first.Add(RefreshInterval)
	updated, err := c.Refresh(context.Background(), later)
	if err != nil {
		t.Fatal(err)
	}
	if updated {
		t.Error("an unchanged table must report no update")
	}
	// A confirmed-current copy is not stale, so the clock has to move even
	// though nothing was downloaded.
	if got := c.Status().FetchedAt; !got.Equal(later) {
		t.Errorf("FetchedAt = %v, want the 304 to stamp it %v", got, later)
	}
	if c.Stale(later) {
		t.Error("a just-confirmed copy must not read as stale")
	}
}

// A source that starts answering with something unparseable must leave the
// last good copy in place: a broken upstream should cost freshness, not the
// ability to price anything at all.
func TestRefreshKeepsTheLastGoodCopyOnBadPayload(t *testing.T) {
	dir := t.TempDir()
	good := serve(t, sampleTable, `"v1"`, nil)
	c := Open(dir)
	withSource(t, c, good)
	if _, err := c.Refresh(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}

	broken := serve(t, "<html>502 Bad Gateway</html>", `"v2"`, nil)
	withSource(t, c, broken)
	if _, err := c.Refresh(context.Background(), time.Now()); err == nil {
		t.Fatal("a malformed payload must be reported as an error")
	}
	if _, ok := c.Lookup("claude-opus-5"); !ok {
		t.Error("the previous good table must survive a failed refresh")
	}
	if _, ok := Open(dir).Lookup("claude-opus-5"); !ok {
		t.Error("the on-disk copy must not have been overwritten with the bad payload")
	}
}

// Missing pricing must never be an error a caller has to handle: a review runs
// the same with or without it.
func TestOpenOnEmptyDirDegradesQuietly(t *testing.T) {
	c := Open(filepath.Join(t.TempDir(), "does-not-exist"))
	if _, ok := c.Lookup("claude-opus-5"); ok {
		t.Error("an empty cache must price nothing")
	}
	if got := c.Status().Models; got != 0 {
		t.Errorf("models = %d, want 0", got)
	}
	if !c.Stale(time.Now()) {
		t.Error("a never-fetched cache must read as stale")
	}
}
