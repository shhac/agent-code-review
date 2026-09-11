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

// DiffFn fetches a PR's per-file line counts. A seam so the scheduler's tests
// never shell out to gh.
type DiffFn func(ctx context.Context, repo string, number int) (discover.PRDiff, error)

// AttrsFn fetches the repo's .gitattributes declarations covering these files.
type AttrsFn func(ctx context.Context, repo, ref string, files []score.FileStat) (map[string]string, error)

// fetchDiff reads the PR's size and works out how much of it counts, at claim
// time. Never returns an error: scoring is an enrichment, exactly as pricing
// is, and a rate limit or a network blip must cost a score rather than a
// review.
//
// A failure here leaves the row unscored AND with no diff recorded, which
// `score recompute` cannot repair: it derives from the stored counts and never
// re-fetches, and DeriveScore rightly refuses a row whose size was never
// measured rather than reading its zeroes as a real zero. So the row stays
// NULL until something re-fetches the diff, which nothing does yet. It is
// visible (`score ls --missing`) rather than silently wrong, but it is not
// self-healing, and the comment here used to claim otherwise.
// fetchDiff returns nil when there is nothing to score from, which is the
// whole of "scoring is off, or the fetch failed" in one value. It used to be a
// struct pairing the stats with an ok bool, which said the same thing in a
// type that had to be declared, and left the enabled-check stated twice.
func (s *Scheduler) fetchDiff(ctx context.Context, cfg config.Config, c store.Candidate) *store.DiffStats {
	if !cfg.ScoringEnabled(c.Repo) {
		return nil
	}
	diff, err := s.diffFn(ctx, c.Repo, c.Number)
	if err != nil {
		s.logf("review %s#%d: diff stats unavailable, leaving it unscored: %v", c.Repo, c.Number, err)
		return nil
	}

	stats := store.DiffStats{
		Additions:    diff.Additions,
		Deletions:    diff.Deletions,
		ChangedFiles: diff.ChangedFiles,
		DiffSHA:      diff.HeadSHA,
	}

	// A truncated file list cannot support exclusion: we would be scoring the
	// first 3000 files and silently calling the rest zero. Fall back to the
	// raw totals, which are complete whatever the listing did.
	if diff.Truncated {
		s.logf("review %s#%d: GitHub truncated the file list at %d files; scoring the raw totals with no exclusions",
			c.Repo, c.Number, len(diff.Files))
		stats.ScoredAdditions, stats.ScoredDeletions = diff.Additions, diff.Deletions
		return &stats
	}

	var attrs score.Attrs
	if cfg.UseGitattributes(c.Repo) {
		byDir, err := s.attrsFn(ctx, c.Repo, "HEAD", diff.Files)
		if err != nil {
			// The repo's declarations are an enrichment on an enrichment. Not
			// reading them means counting generated lines, which is a worse
			// score rather than no score.
			s.logf("review %s#%d: could not read .gitattributes, counting every file: %v", c.Repo, c.Number, err)
		} else {
			attrs = score.ParseAttrs(byDir)
		}
	}

	totals := score.NewExclusions(attrs, cfg.ExcludePaths(c.Repo)).Apply(diff.Files)
	stats.ScoredAdditions = totals.Additions
	stats.ScoredDeletions = totals.Deletions
	stats.ExcludedFiles = totals.ExcludedFiles
	return &stats
}

// applyScore attaches the diff and the points to a history record, after the
// verdict is known. Pure arithmetic plus one cheap store read: no network, so
// nothing here can widen the window before Complete.
func (s *Scheduler) applyScore(ctx context.Context, cfg config.Config, rec *store.Review, diff *store.DiffStats) {
	// nil covers both "scoring is off" and "the fetch failed". The enabled
	// check lives in fetchDiff alone: re-asking here read as a second gate but
	// could never fire, since reviewOne passes ONE cfg snapshot to both calls.
	if diff == nil {
		return
	}
	rec.Diff = *diff

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
