package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// Synthetic engine markers: history rows produced without invoking a review
// engine record their provenance in the engine column instead.
const (
	EnginePrecheck = "precheck" // scheduler's pre-review candidacy recheck
	EngineManual   = "manual"   // `queue skip` by a human
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
	// TokensUsed split by kind, when the engine reports one. All zero means
	// it reported only a total.
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheCreationTokens int `json:"cache_creation_tokens"`
	CacheReadTokens     int `json:"cache_read_tokens"`
}

// FreshTokens is the tokens a review actually processed, excluding context it
// re-read from cache. It is the only token figure comparable across engines:
// claude reports cached reads and they dominate a long session (millions
// against a codex review's ~130k total), so charting raw totals compares two
// different measurements.
//
// An engine that reports no split falls back to its total, on the basis that
// a single reported figure is a fresh-token count. That holds for codex by
// inspection — a 22-turn review reports ~150k, which cumulative per-turn
// re-reads could not be — but it is inference from magnitude, not a
// documented guarantee.
func (r Review) FreshTokens() int {
	if r.InputTokens+r.OutputTokens+r.CacheCreationTokens+r.CacheReadTokens == 0 {
		return r.TokensUsed
	}
	return r.InputTokens + r.OutputTokens + r.CacheCreationTokens
}

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
