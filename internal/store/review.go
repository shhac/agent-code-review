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

// Review records one completed outcome for a PR at a specific head SHA —
// including SKIPPED and ERROR, which live in history like everything else.
// Title and Author are snapshots of the PR at completion time so the History
// page can render outcomes like queue items without a gh round-trip.
type Review struct {
	Repo         string    `json:"repo"`
	Number       int       `json:"number"`
	LogKey       string    `json:"log_key,omitempty"` // deterministic URL key for selecting this exact history row's log
	Title        string    `json:"title"`
	Author       string    `json:"author"`
	HeadSHA      string    `json:"head_sha"`
	Verdict      string    `json:"verdict"` // APPROVED|COMMENTED|REQUESTED_CHANGES|SKIPPED|ERROR
	Engine       string    `json:"engine"`
	Model        string    `json:"model,omitempty"`  // managed Codex model; empty means Codex selected its default
	Effort       string    `json:"effort,omitempty"` // managed Codex reasoning effort; empty means model default
	ReviewedAt   time.Time `json:"reviewed_at"`
	DurationSecs int       `json:"duration_secs"`      // claim-to-completion elapsed; 0 when unknown
	WorkDir      string    `json:"work_dir,omitempty"` // engine workspace used, kept for postmortem log access
	TokensUsed   int       `json:"tokens_used"`        // engine-reported token spend; 0 when unknown
}

// ReviewFrom snapshots a candidate's identity into a history record — the
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

func ReviewLogRefFromReview(r Review) ReviewLogRef {
	key := r.LogKey
	if key == "" {
		key = ReviewLogKey(r)
	}
	return ReviewLogRef{Repo: r.Repo, Number: r.Number, LogKey: key}
}

// ReviewLogKey is the stable, non-secret URL token for a history row's log.
func ReviewLogKey(r Review) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%d\x00%s\x00%d",
		r.Repo, r.Number, r.HeadSHA, r.Verdict, r.Engine,
		r.ReviewedAt.UTC().Format(time.RFC3339Nano), r.DurationSecs, r.WorkDir, r.TokensUsed)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// realVerdicts is the single source of the "actual posted review" set — the
// outcomes that count as "reviewed at this SHA" for Refreshed detection.
// SKIPPED and ERROR deliberately aren't in it: new commits (or a manual
// re-add) must be able to re-surface those PRs. Both IsRealVerdict and the
// driver's SQL filter derive from this list. (The engine's Decision*
// constants in the review package mirror these strings; review imports
// store, so the vocabulary can't reference them from here.)
var realVerdicts = []string{"APPROVED", "COMMENTED", "REQUESTED_CHANGES"}

// IsRealVerdict reports whether v is an actual posted review — the predicate
// behind LastReview's history filter.
func IsRealVerdict(v string) bool {
	for _, rv := range realVerdicts {
		if v == rv {
			return true
		}
	}
	return false
}
