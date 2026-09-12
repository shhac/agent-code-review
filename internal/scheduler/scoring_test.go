package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/discover"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/score"
	"github.com/shhac/agent-code-review/internal/store"
)

// scoringStore is fakeSchedStore with an answer for ScoreContext, which is the
// only extra call the scoring path makes.
type scoringStore struct {
	*fakeSchedStore
	sc    store.ScoreContext
	scErr error
}

func (s *scoringStore) ScoreContext(_ context.Context, _ string, _ int, _ string, _ time.Time) (store.ScoreContext, error) {
	return s.sc, s.scErr
}

func scoringDeps(fs *scoringStore, diff discover.PRDiff, diffErr error, attrs map[string]string, cfg config.Config) Deps {
	return Deps{
		Config: func() config.Config { return cfg },
		Diff: func(context.Context, string, int) (discover.PRDiff, error) {
			return diff, diffErr
		},
		Attrs: func(context.Context, string, string, []score.FileStat) (map[string]string, error) {
			return attrs, nil
		},
	}
}

// runScored drives one review through the scheduler and returns the history
// row it completed.
func runScored(t *testing.T, fs *scoringStore, diff discover.PRDiff, diffErr error, attrs map[string]string, cfg config.Config, verdict string) store.Review {
	t.Helper()
	fe := &fakeEngine{verdict: review.Verdict{Decision: verdict}}
	d := scoringDeps(fs, diff, diffErr, attrs, cfg)
	d.Store = fs
	d.NewEngine = fixedEngine(fe)
	s := newScheduler(d)

	c := store.Candidate{Repo: "o/r", Number: 5, Author: "alice", HeadSHA: "sha-head"}
	m, err := s.store.AuthorGroup(context.Background(), c.Repo, c.Author)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.reviewOne(context.Background(), pending{
		candidate: c, policy: cfg.ResolvePolicy(c.Repo, c.Author, m),
	}, cfg, fe); err != nil {
		t.Fatal(err)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if len(fs.completed) != 1 {
		t.Fatalf("completed %d rows, want 1", len(fs.completed))
	}
	return fs.completed[0]
}

func baseCfg() config.Config {
	return config.Config{Review: config.ReviewSettings{MainPrompt: "MAIN"}}
}

func smallDiff() discover.PRDiff {
	return discover.PRDiff{
		HeadSHA: "sha-head", Additions: 40, Deletions: 10, ChangedFiles: 1,
		Files: []score.FileStat{{Path: "main.go", Additions: 40, Deletions: 10}},
	}
}

func TestScoreLandsOnTheCompletedRow(t *testing.T) {
	fs := &scoringStore{fakeSchedStore: &fakeSchedStore{}, sc: store.ScoreContext{Attempt: 1}}
	got := runScored(t, fs, smallDiff(), nil, nil, baseCfg(), store.VerdictApproved)

	// 55 churn (removals weigh 1.5), just past the "small" anchor at 50, so
	// the rate is 1.47x on the way down rather than a flat tier figure.
	if !got.Score.Scored() || got.Score.Points() != 139 {
		t.Errorf("score = %+v, want 139", got.Score)
	}
	if got.Score.Source != store.ScoreDerived {
		t.Errorf("source = %q, want %q", got.Score.Source, store.ScoreDerived)
	}
	if got.Score.Rules != baseCfg().ResolveScoring("o/r").Hash() {
		t.Errorf("rules hash = %q, want the resolved ruleset's", got.Score.Rules)
	}
	if got.Diff.Additions != 40 || got.Diff.ScoredAdditions != 40 {
		t.Errorf("diff = %+v, want raw and scored both +40", got.Diff)
	}
}

// Generated files come out of the scored counts but stay in the raw ones, so
// the row can explain why it disagrees with the PR page.
func TestGeneratedFilesAreExcludedFromTheScoredCounts(t *testing.T) {
	diff := discover.PRDiff{
		HeadSHA: "sha-head", Additions: 8040, Deletions: 2010, ChangedFiles: 2,
		Files: []score.FileStat{
			{Path: "main.go", Additions: 40, Deletions: 10},
			{Path: "package-lock.json", Additions: 8000, Deletions: 2000},
		},
	}
	fs := &scoringStore{fakeSchedStore: &fakeSchedStore{}, sc: store.ScoreContext{Attempt: 1}}
	got := runScored(t, fs, diff, nil, map[string]string{"": "package-lock.json linguist-generated\n"}, baseCfg(), store.VerdictApproved)

	if got.Diff.Additions != 8040 {
		t.Errorf("raw additions = %d, want GitHub's 8040 preserved", got.Diff.Additions)
	}
	if got.Diff.ScoredAdditions != 40 || got.Diff.ScoredDeletions != 10 {
		t.Errorf("scored = +%d/-%d, want +40/-10", got.Diff.ScoredAdditions, got.Diff.ScoredDeletions)
	}
	if got.Diff.ExcludedFiles != 1 {
		t.Errorf("excluded files = %d, want 1", got.Diff.ExcludedFiles)
	}
	if got.Score.Points() != 139 {
		t.Errorf("score = %d, want the medium-bucket 139 rather than a huge-bucket score", got.Score.Points())
	}
}

// A second verdict at the same head is discussion, not new code.
func TestDiscussionRereviewScoresZero(t *testing.T) {
	fs := &scoringStore{
		fakeSchedStore: &fakeSchedStore{},
		sc:             store.ScoreContext{Attempt: 1, ReviewedAtThisHead: true},
	}
	got := runScored(t, fs, smallDiff(), nil, nil, baseCfg(), store.VerdictApproved)

	if !got.Score.Scored() {
		t.Fatal("it should be scored, with the answer zero")
	}
	if got.Score.Points() != 0 {
		t.Errorf("score = %d, want 0: replying to the bot is not new work", got.Score.Points())
	}
}

// Scoring is an enrichment: everything that can go wrong must cost a score,
// never a review.
func TestScoringFailuresLeaveTheRowUnscoredButComplete(t *testing.T) {
	cases := []struct {
		name    string
		diff    discover.PRDiff
		diffErr error
		sc      store.ScoreContext
		scErr   error
	}{
		{name: "diff fetch failed", diffErr: errors.New("rate limited"), sc: store.ScoreContext{Attempt: 1}},
		{name: "attempt lookup failed", diff: smallDiff(), scErr: errors.New("store down"), sc: store.ScoreContext{Attempt: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := &scoringStore{fakeSchedStore: &fakeSchedStore{}, sc: tc.sc, scErr: tc.scErr}
			got := runScored(t, fs, tc.diff, tc.diffErr, nil, baseCfg(), store.VerdictApproved)

			if got.Verdict != store.VerdictApproved {
				t.Errorf("verdict = %q: the review itself must still be recorded", got.Verdict)
			}
			if got.Score.Scored() {
				t.Errorf("score = %+v, want unscored so a recompute can find it", got.Score)
			}
		})
	}
}

// The head moving mid-review means the figures describe code this review never
// saw. Crediting them would be inventing a number.
func TestHeadMovingMidReviewLeavesItUnscored(t *testing.T) {
	diff := smallDiff()
	diff.HeadSHA = "sha-newer"
	fs := &scoringStore{fakeSchedStore: &fakeSchedStore{}, sc: store.ScoreContext{Attempt: 1}}
	got := runScored(t, fs, diff, nil, nil, baseCfg(), store.VerdictApproved)

	if got.Score.Scored() {
		t.Errorf("score = %+v, want unscored when the head moved", got.Score)
	}
	// The figures are still recorded, tagged with the revision they describe,
	// so a recompute can decide what to do with them.
	if got.Diff.DiffSHA != "sha-newer" {
		t.Errorf("diff_sha = %q, want the revision the stats describe", got.Diff.DiffSHA)
	}
}

// A truncated file list cannot support exclusion, so it falls back to the raw
// totals rather than calling 20,000 unlisted lines zero.
func TestTruncatedFileListFallsBackToRawTotals(t *testing.T) {
	diff := discover.PRDiff{
		HeadSHA: "sha-head", Additions: 50000, Deletions: 1000, ChangedFiles: 4000,
		Files:     []score.FileStat{{Path: "a.go", Additions: 10}},
		Truncated: true,
	}
	fs := &scoringStore{fakeSchedStore: &fakeSchedStore{}, sc: store.ScoreContext{Attempt: 1}}
	got := runScored(t, fs, diff, nil, nil, baseCfg(), store.VerdictApproved)

	if got.Diff.ScoredAdditions != 50000 || got.Diff.ScoredDeletions != 1000 {
		t.Errorf("scored = +%d/-%d, want the raw totals", got.Diff.ScoredAdditions, got.Diff.ScoredDeletions)
	}
	if got.Diff.ExcludedFiles != 0 {
		t.Errorf("excluded = %d, want 0: a partial listing cannot support exclusion", got.Diff.ExcludedFiles)
	}
}

func TestScoringCanBeTurnedOff(t *testing.T) {
	off := false
	cfg := baseCfg()
	cfg.Scoring = config.ScoringSettings{Enabled: &off}

	fs := &scoringStore{fakeSchedStore: &fakeSchedStore{}, sc: store.ScoreContext{Attempt: 1}}
	got := runScored(t, fs, smallDiff(), nil, nil, cfg, store.VerdictApproved)

	if got.Score.Scored() {
		t.Errorf("score = %+v, want nothing when scoring is disabled", got.Score)
	}
	if got.Diff.Additions != 0 {
		t.Errorf("diff = %+v, want no fetch at all when scoring is off", got.Diff)
	}
}

// The attempt index rides onto the row so a later recompute lands on the same
// number, and a manual correction can pin one.
func TestAttemptIndexIsFrozenOntoTheRow(t *testing.T) {
	fs := &scoringStore{fakeSchedStore: &fakeSchedStore{}, sc: store.ScoreContext{Attempt: 3}}
	got := runScored(t, fs, smallDiff(), nil, nil, baseCfg(), store.VerdictApproved)

	if got.Score.Attempt == nil || *got.Score.Attempt != 3 {
		t.Fatalf("attempt = %v, want 3", got.Score.Attempt)
	}
	// 139 * 0.4^2 = 22.2 -> 22
	if got.Score.Points() != 22 {
		t.Errorf("score = %d, want 22 (the third revision's decay)", got.Score.Points())
	}
}
