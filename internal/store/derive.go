package store

import (
	"time"

	"github.com/shhac/agent-code-review/internal/score"
)

// DeriveScore turns a review and its history context into the frozen score
// that goes on its history row.
//
// THE single derivation. It exists because there are two paths that score a
// review -- the scheduler at completion, and `score recompute` re-deriving one
// later -- and they must not disagree. They already had: each carried its own
// copy of this sequence, and when the scheduler grew a guard against a head
// that moved mid-review, recompute did not. The scheduler leaves those rows
// unscored WITH their diff figures recorded, which is exactly what
// `recompute --missing` selects, so the recovery path scored them off a diff
// describing code the review never saw -- the one outcome the guard existed to
// prevent. A shared arithmetic helper was never enough: the POLICY around the
// arithmetic is what drifted.
//
// It lives in store rather than in score because it returns a ScoreRecord and
// reads a Review; score deliberately depends on neither (see that package's
// comment), and store -> score is the direction the layering already allows.
//
// ok=false means this review must not be scored at all, as distinct from
// scoring zero: the caller leaves the row NULL so a later sweep can retry it.
func DeriveScore(rules score.Rules, sc ScoreContext, r Review, at time.Time) (ScoreRecord, bool) {
	if !IsRealVerdict(r.Verdict) {
		return ScoreRecord{}, false
	}
	// No diff was ever fetched (a rate limit, a network blip). Scoring it
	// anyway reads the row's zeroed counts as churn 0 and freezes a score of
	// 0 -- which then LOOKS scored, so `--missing` never revisits it and a real
	// PR's points are gone for good. Leave it NULL and recoverable.
	if !r.Diff.Recorded() {
		return ScoreRecord{}, false
	}
	if r.Diff.Stale(r.HeadSHA) {
		return ScoreRecord{}, false
	}

	// A second real verdict at the same head is a discussion re-review:
	// somebody replied to the bot and it answered. No new code was written, so
	// there is nothing to pay for, and charging a decay step would make
	// talking to the reviewer cost the author points.
	res := score.Compute(rules, score.Input{
		Additions: r.Diff.ScoredAdditions,
		Deletions: r.Diff.ScoredDeletions,
		Verdict:   r.Verdict,
		Attempt:   sc.Attempt,
	})
	points := res.Score
	if sc.ReviewedAtThisHead {
		points = 0
	}

	attempt := sc.Attempt
	if at.IsZero() {
		at = time.Now()
	}
	return ScoreRecord{
		Score:   &points,
		Source:  ScoreDerived,
		Rules:   rules.Hash(),
		Bucket:  res.Bucket,
		Attempt: &attempt,
		At:      at,
	}, true
}
