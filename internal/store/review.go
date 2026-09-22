package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/shhac/agent-code-review/internal/score"
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
	// Steering is the instruction that shaped this review, copied off the queue
	// row as it was retired. Same shape as Candidate.Steering deliberately: it
	// is the same fact, so the dashboard renders it one way.
	//
	// nil means NOT RECORDED rather than "not steered". Rows written before
	// these columns existed cannot say which they were, and their messages went
	// with the queue rows they lived on.
	Steering *Steering `json:"steering,omitempty"`
	// PolicyViolation marks an outcome the agent was not permitted to reach:
	// APPROVED for an author whose resolved policy forbids approving. The
	// approval permission reaches the agent only as prompt text and the agent
	// posts to GitHub itself, so this is the record that the instruction was
	// not followed — detection, not prevention. Verdict still says what
	// actually happened; this says what we asked for.
	PolicyViolation bool `json:"policy_violation,omitempty"`
	// Diff is the PR's line counts, raw and after excluding generated files,
	// fetched at CLAIM time rather than here. DiffSHA says which revision they
	// describe: a head can advance mid-review (Complete's DELETE is gated on
	// head_sha for that reason), and lines the review never saw must not be
	// credited to it.
	Diff DiffStats `json:"diff"`
	// Score is the author's points for this review, frozen with the ruleset
	// hash that produced it. Its Score field is nil when the row was never
	// scored, which is NOT the same as a score of zero.
	Score ScoreRecord `json:"score"`
	// DiffFiles is the per-file detail the score was measured from, so an
	// exclusion policy can be re-applied without asking GitHub again.
	//
	// json:"-" on purpose. It is written with the row and read back only by
	// the scoring commands that need it; putting it on the wire would add
	// megabytes to a history page for data no reader of that page wants. Read
	// it deliberately via ReviewFiles rather than expecting it on every scan.
	DiffFiles []score.FileStat `json:"-"`
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
		// The steering goes into history because it is about to leave the
		// queue: Complete retires the row and the instruction with it, so this
		// is the last moment anything can record what the agent was told.
		Steering: c.Steering,
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
