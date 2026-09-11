package scheduler

// Author scoring: turning a finished review into points for the PR's author.
//
// The fetch happens BEFORE the engine runs, not after the verdict. That
// ordering is the whole design and it is not an optimisation:
//
// The window between a verdict coming back and Complete recording it is
// microseconds today, and everything about crash recovery depends on it
// staying that way. Put ~31 sequential gh subprocesses in there and a daemon
// death mid-scoring no longer loses a score, it loses the REVIEW: Reconcile
// appends an ERROR row, the next claim's recheck sees we already reviewed this
// head on GitHub, and records SKIPPED. The verdict, its token split and its
// cost record are gone, for a review that already posted. gh subprocesses are
// also deliberately left in our process group, so any Ctrl-C landing in that
// window would kill the fetch too.
//
// Fetching at claim time costs nothing by comparison: it is before the
// expensive part, a failure there is just a claim released, and the diff we
// read is the one the engine is about to review. Scoring after the verdict is
// then pure arithmetic, and the score rides into the SAME atomic history
// insert Complete already does.

import (
	"context"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/discover"
	"github.com/shhac/agent-code-review/internal/score"
	"github.com/shhac/agent-code-review/internal/store"
)

// measured is what the claim-time measurement hands to the post-verdict
// scorer: nil when there is nothing to score from.
type measured struct {
	stats store.DiffStats
	files []score.FileStat
}

// fetchDiff measures the PR at claim time, before the engine runs.
//
// Never returns an error: scoring is an enrichment, exactly as pricing is, and
// a rate limit or a network blip must cost a score rather than a review.
//
// A failure here leaves the row unscored AND with no measurement recorded,
// which `score recompute` cannot repair, because it re-applies policy to a
// stored measurement rather than taking a new one. `score refetch` is the
// repair for that, and it can only work while the PR is still at the head we
// reviewed.
func (s *Scheduler) fetchDiff(ctx context.Context, cfg config.Config, c store.Candidate) *measured {
	if !cfg.ScoringEnabled(c.Repo) {
		return nil
	}
	m, err := discover.Measurer{Diff: s.diffFn, Attrs: s.attrsFn}.Measure(ctx, cfg, c.Repo, c.Number)
	if err != nil {
		s.logf("review %s#%d: diff stats unavailable, leaving it unscored: %v", c.Repo, c.Number, err)
		return nil
	}
	for _, note := range m.Notes {
		s.logf("review %s#%d: %s", c.Repo, c.Number, note)
	}
	return &measured{stats: m.Stats, files: m.Files}
}

// applyScore attaches the diff and the points to a history record, after the
// verdict is known. Pure arithmetic plus one cheap store read: no network, so
// nothing here can widen the window before Complete.
func (s *Scheduler) applyScore(ctx context.Context, cfg config.Config, rec *store.Review, diff *measured) {
	// nil covers both "scoring is off" and "the fetch failed". The enabled
	// check lives in fetchDiff alone: re-asking here read as a second gate but
	// could never fire, since reviewOne passes ONE cfg snapshot to both calls.
	if diff == nil {
		return
	}
	rec.Diff = diff.stats
	rec.DiffFiles = diff.files

	// SKIPPED and ERROR are outcomes of our own machinery, not feedback to an
	// author, and DeriveScore refuses them too. Returning early here keeps
	// them from costing a ScoreContext query they can never use.
	// (resumableRun.resolve returns nil whenever a report parsed, so
	// reviewErr != nil iff the verdict is ERROR; this gate and
	// retryAfterError's bounded-retry check would break together if a future
	// driver ever returned a real verdict alongside an error.)
	if !store.IsRealVerdict(rec.Verdict) {
		return
	}
	// The head moved while the review ran. Complete already handles this for
	// the queue row; here it means the figures describe code this review never
	// saw. DeriveScore refuses it too; this only explains why in the log.
	if rec.Diff.Stale(rec.HeadSHA) {
		s.logf("review %s#%d: head moved from %s to %s while reviewing, leaving it unscored",
			rec.Repo, rec.Number, short(rec.Diff.DiffSHA), short(rec.HeadSHA))
		return
	}

	sc, err := s.store.ScoreContext(ctx, rec.Repo, rec.Number, rec.HeadSHA, rec.ReviewedAt)
	if err != nil {
		s.logf("review %s#%d: could not resolve the attempt index, leaving it unscored: %v", rec.Repo, rec.Number, err)
		return
	}
	if sc.ReviewedAtThisHead {
		s.logf("review %s#%d: already reviewed at this revision, scoring it 0 (discussion, not new work)", rec.Repo, rec.Number)
	}

	// Not-ok leaves rec.Score zero, which reads back as NULL: never scored,
	// and so still reachable by `score recompute --missing`.
	if derived, ok := store.DeriveScore(cfg.ResolveScoring(rec.Repo), sc, *rec, time.Now()); ok {
		rec.Score = derived
	}
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
