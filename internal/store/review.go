package store

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Synthetic engine markers: history rows produced without invoking a review
// engine record their provenance in the engine column instead.
const (
	EnginePrecheck  = "precheck"  // scheduler's pre-review candidacy recheck
	EngineManual    = "manual"    // `queue skip` by a human
	EngineAbandoned = "abandoned" // a claim released after the daemon died mid-review
)

// Review records one completed outcome for a PR at a specific head SHA,
// including SKIPPED and ERROR, which live in history like everything else.
// Title and Author are snapshots of the PR at completion time so the History
// page can render outcomes like queue items without a gh round-trip.
type Review struct {
	Repo          string    `json:"repo"`
	Number        int       `json:"number"`
	LogKey        string    `json:"log_key,omitempty"` // deterministic URL key for selecting this exact history row's log
	Title         string    `json:"title"`
	Author        string    `json:"author"`
	HeadSHA       string    `json:"head_sha"`
	Verdict       string    `json:"verdict"` // APPROVED|COMMENTED|REQUESTED_CHANGES|SKIPPED|ERROR
	Engine        string    `json:"engine"`
	Model         string    `json:"model,omitempty"`          // managed model; empty means the engine selected its default
	Effort        string    `json:"effort,omitempty"`         // managed reasoning effort; empty means model default
	EngineVersion string    `json:"engine_version,omitempty"` // version of the engine CLI that ran this review
	ReviewedAt    time.Time `json:"reviewed_at"`
	DurationSecs  int       `json:"duration_secs"`      // claim-to-completion elapsed; 0 when unknown
	WorkDir       string    `json:"work_dir,omitempty"` // engine workspace used, kept for postmortem log access
	TokensUsed    int       `json:"tokens_used"`        // engine-reported token spend; 0 when unknown
	// CostUSD is the engine's API-rate valuation of the run, NOT money
	// charged: on a subscription it is what those tokens would have cost at
	// API rates. It is the only per-review spend signal an engine reports,
	// and the unit `claude.max_budget_usd` is compared against. 0 when the
	// engine reports none (codex prints only a token trailer).
	CostUSD float64 `json:"cost_usd"`
	// EstCostUSD is our own valuation of the run: its token classes priced
	// against the model's rates. Frozen at completion time rather than derived
	// on read, because rates change and what a review cost is what it cost
	// then, not what the same tokens would cost today.
	//
	// 0 means no estimate was possible (no price table, an unlisted model, or
	// a row recorded before the class split existed). It never means free.
	EstCostUSD float64 `json:"est_cost_usd"`
	// The token classes, which are priced differently and so are recorded
	// apart: a cached read costs about a tenth of fresh input and a sixtieth
	// of output. Each driver maps its engine's reporting onto them, so nothing
	// downstream branches on the engine.
	//
	// FreshTokens is Input+Output+CacheWrite, the only figure comparable
	// between engines (re-reads dominate a long session, so a claude review's
	// raw total runs ~28x a codex review's, almost all of it cache). 0 means
	// unknown rather than no work: rows recorded before these columns cannot
	// be recovered, and aggregates leave them out the way they leave out
	// unpriced rows.
	//
	// ReasoningTokens is part of OutputTokens, not an addition to it.
	FreshTokens      int `json:"fresh_tokens"`
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
	// UsageRaw is what the engine actually said about usage, verbatim, as a
	// JSON array with one entry per invocation. The escape hatch: claude
	// reports 5m/1h cache-write tiers priced differently, server tool calls
	// billed separately, and per-message iterations, none of which are
	// modelled above. Keeping the raw payload means a later pricing or
	// analytics question is a query rather than a migration and a data gap.
	UsageRaw string `json:"usage_raw,omitempty"`
}

// EffectiveCostUSD is the run's spend however it is best known: the engine's
// own figure when it reported one, otherwise ours. Only claude values its own
// runs, so without the fallback every codex review reads as free.
//
// Deliberately expressible in SQL as
// COALESCE(NULLIF(cost_usd, 0), est_cost_usd) — the fresh-token heuristic this
// replaces was not, which is how a Go aggregate and a SQL one came to disagree
// about the same history.
func (r Review) EffectiveCostUSD() float64 {
	if r.CostUSD > 0 {
		return r.CostUSD
	}
	return r.EstCostUSD
}

// CostEstimated says whether EffectiveCostUSD came from our rates rather than
// the engine. A total mixing the two has to be able to say how much of it is
// inferred.
func (r Review) CostEstimated() bool { return r.CostUSD == 0 && r.EstCostUSD > 0 }

// ReviewFrom snapshots a candidate's identity into a history record: the
// single place the Candidate→Review field fan-out lives, so a new snapshot
// field cannot be added to one Complete call site and missed at another.
// started is when the review began (the claim time); the zero value records
// an unknown duration as 0 (manual skips, backfilled rows).
func ReviewFrom(c Candidate, verdict, engine string, started time.Time) Review {
	duration := 0
	if !started.IsZero() {
		duration = int(time.Since(started).Seconds())
	}
	return Review{
		Repo:         c.Repo,
		Number:       c.Number,
		Title:        c.Title,
		Author:       c.Author,
		HeadSHA:      c.HeadSHA,
		Verdict:      verdict,
		Engine:       engine,
		ReviewedAt:   time.Now(),
		DurationSecs: duration,
		WorkDir:      c.WorkDir,
	}
}

// ReviewLogRef identifies the review-log view for a PR. LogKey empty means
// the live queue row if present, else the latest recorded outcome; LogKey set
// means one exact history row.
type ReviewLogRef struct {
	Repo   string
	Number int
	LogKey string
}

// ReviewLogKey is the stable, non-secret URL token for a history row's log.
func ReviewLogKey(r Review) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%d\x00%s\x00%d",
		r.Repo, r.Number, r.HeadSHA, r.Verdict, r.Engine,
		r.ReviewedAt.UTC().Format(time.RFC3339Nano), r.DurationSecs, r.WorkDir, r.TokensUsed)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Verdict vocabulary: the canonical strings for every recordable outcome.
// The review package's Decision* constants alias these (store is the layer
// both sides already import), so the vocabulary cannot drift.
const (
	VerdictApproved         = "APPROVED"
	VerdictCommented        = "COMMENTED"
	VerdictRequestedChanges = "REQUESTED_CHANGES"
	VerdictSkipped          = "SKIPPED"
	VerdictWorking          = "WORKING" // engine-intermediate only; never recorded
	VerdictError            = "ERROR"
)

// realVerdicts is the single source of the "actual posted review" set: the
// outcomes that count as "reviewed at this SHA" for Refreshed detection.
// SKIPPED and ERROR deliberately aren't in it: new commits (or a manual
// re-add) must be able to re-surface those PRs. Both IsRealVerdict and the
// driver's SQL filter derive from this list.
var realVerdicts = []string{VerdictApproved, VerdictCommented, VerdictRequestedChanges}

// IsRealVerdict is the Go mirror of the SQL predicate LastReview actually
// filters with (realVerdictsSQL): a deliberate lockstep seam, pinned by the
// store and scheduler tests so the two derivations of realVerdicts cannot
// drift.
func IsRealVerdict(v string) bool {
	for _, rv := range realVerdicts {
		if v == rv {
			return true
		}
	}
	return false
}

// Defaults and ceiling for one page of history. The ceiling is a transfer
// bound, not a search bound: a search matches the whole table server-side and
// only its current page crosses the wire, so capping the page no longer caps
// what can be found.
const (
	defaultReviewLimit = 50
	maxReviewLimit     = 500
)

// ReviewSort names an ordering. One value today; it is a field rather than an
// assumption so adding "oldest first" or "most expensive first" later is a new
// constant, not a new cursor format.
type ReviewSort string

const SortNewest ReviewSort = "newest"

// Normalise resolves the empty sort to the default and rejects one this build
// does not implement. Rejecting matters: an unrecognised sort silently served
// as "newest" is a page that answers a different question than it was asked,
// and the ordering is invisible in the rows themselves.
func (s ReviewSort) Normalise() (ReviewSort, error) {
	switch s {
	case "", SortNewest:
		return SortNewest, nil
	default:
		return "", fmt.Errorf("unknown sort %q", string(s))
	}
}

// cursorVersion is bumped when a field's MEANING changes, not when one is
// added: an added field is absent from an old cursor and takes its zero value,
// which is why the payload is JSON rather than a positional string.
const cursorVersion = 1

// ReviewCursor is a self-describing position in a result set: which row to
// resume after, and which query and ordering produced it.
//
// It carries the query and the sort, not just the row, so the server can
// refuse a cursor that belongs to a different search. Without that, a cursor
// outliving the search it came from (a reload, a shared URL, a stale tab)
// pages through rows from the old result set under the new query's heading,
// and nothing on screen says so.
//
// All three position fields, because reviewed_at is not unique: 676 of this
// history's timestamps are shared by two or more rows, so a cursor carrying
// the timestamp alone would either skip the rest of a tied group or repeat it.
// Repo and Number break the tie, and the ORDER BY names the same three columns
// in the same order so the comparison and the sort cannot disagree.
type ReviewCursor struct {
	Version    int        `json:"v"`
	Sort       ReviewSort `json:"sort,omitempty"`
	Text       string     `json:"q,omitempty"`
	ReviewedAt time.Time  `json:"at"`
	Repo       string     `json:"repo"`
	Number     int        `json:"n"`
}

// IsZero reports the first page: no row to resume after.
func (c ReviewCursor) IsZero() bool { return c.ReviewedAt.IsZero() }

// String encodes a cursor for a URL. Opaque to the browser on purpose: it
// holds the value and hands it back, and nothing outside this file should be
// composing one out of a timestamp it guessed.
func (c ReviewCursor) String() string {
	if c.IsZero() {
		return ""
	}
	c.Version = cursorVersion
	raw, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// ParseReviewCursor decodes what String produced, and checks it belongs to the
// search being run. A malformed or foreign cursor is an error rather than a
// silent first page: paging is the one place where quietly ignoring a
// parameter shows the reader a page they did not ask for and gives them no way
// to tell.
func ParseReviewCursor(s, text string, sort ReviewSort) (ReviewCursor, error) {
	if s == "" {
		return ReviewCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return ReviewCursor{}, fmt.Errorf("cursor is not valid base64: %w", err)
	}
	var c ReviewCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return ReviewCursor{}, fmt.Errorf("cursor is not valid JSON: %w", err)
	}
	if c.Version != cursorVersion {
		return ReviewCursor{}, fmt.Errorf("cursor is version %d, want %d", c.Version, cursorVersion)
	}
	if c.Text != text {
		return ReviewCursor{}, fmt.Errorf("cursor belongs to a different search")
	}
	want, err := sort.Normalise()
	if err != nil {
		return ReviewCursor{}, err
	}
	if c.Sort != want {
		return ReviewCursor{}, fmt.Errorf("cursor is sorted %q, want %q", string(c.Sort), string(want))
	}
	return c, nil
}

// ReviewQuery selects one page of history. The zero value is a valid query:
// the newest defaultReviewLimit rows, unfiltered.
type ReviewQuery struct {
	// Text matches anywhere in "repo#number title author verdict",
	// case-insensitively. Empty matches every row.
	Text string
	// Limit is the page size, clamped to maxReviewLimit. Zero or negative
	// takes the default rather than returning nothing, so a caller that
	// forgets to set it gets a page instead of an empty result that reads as
	// "no matches".
	Limit int
	// Sort is the ordering. Empty takes SortNewest.
	Sort ReviewSort
	// After starts the page strictly after a row from the previous page.
	// The zero cursor means the first page.
	//
	// A cursor rather than an offset because history grows while it is being
	// read: a review completing between two page fetches shifts every later
	// offset by one, so page 2 re-shows the row that page 1 ended on. The
	// cursor names a position in the data instead of a distance from the top,
	// so an insertion above it changes nothing.
	After ReviewCursor
}

// sort is the query's effective ordering. An invalid one cannot reach here:
// the caller normalises before building the query, and the zero value is the
// default rather than an error, so the zero ReviewQuery stays valid.
func (q ReviewQuery) sort() ReviewSort {
	s, err := q.Sort.Normalise()
	if err != nil {
		return SortNewest
	}
	return s
}

func (q ReviewQuery) limit() int {
	if q.Limit <= 0 {
		return defaultReviewLimit
	}
	return min(q.Limit, maxReviewLimit)
}

// ReviewPage is one page of matching history plus the size of the whole match.
type ReviewPage struct {
	Reviews []Review `json:"reviews"`
	// Total counts every row the query matches, not the rows in this page,
	// so a pager can show the last page and the page can say how many results
	// the search actually found.
	Total int `json:"total"`
	// NextCursor starts the following page, empty on the last page.
	NextCursor string `json:"next_cursor,omitempty"`
}
