package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/score"
	"github.com/shhac/agent-code-review/internal/store"
)

// fmt.Sscanf("%d") stops at the end of its format and ignores the rest, so
// "15.5" parsed as 15 and "150x" as 150, both with a nil error. On the manual
// correction path that silently freezes a different number than the operator
// typed, permanently.
func TestParseScoreRejectsTrailingGarbage(t *testing.T) {
	for _, s := range []string{"15.5", "150x", "1 2", "12abc", "", "abc", "--5", "1e3"} {
		if got, err := parseScore(s); err == nil {
			t.Errorf("parseScore(%q) = %d with no error; it must be rejected", s, got)
		}
	}
}

func TestParseScoreAcceptsWholeNumbers(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{{"150", 150}, {"-38", -38}, {"0", 0}, {" 7 ", 7}, {"+12", 12}} {
		got, err := parseScore(tc.in)
		if err != nil {
			t.Errorf("parseScore(%q) errored: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseScore(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// fakeScoreStore records what recompute writes. Unimplemented Store methods
// panic, so an unexpected dependency shows up loudly.
type fakeScoreStore struct {
	store.Store
	rows     []store.Review
	sc       map[int]store.ScoreContext
	written  map[int]store.ScoreRecord
	files    map[int][]score.FileStat // per-row stored measurement, for recount
	measured map[int]store.DiffStats  // what a re-measure wrote back
	failOn   int                      // SetReviewScore returns an error for this PR number
}

func (f *fakeScoreStore) ReviewsToScore(context.Context, store.ScoreQuery) ([]store.Review, error) {
	return f.rows, nil
}

func (f *fakeScoreStore) ScoreContext(_ context.Context, _ string, number int, _ string, _ time.Time) (store.ScoreContext, error) {
	if sc, ok := f.sc[number]; ok {
		return sc, nil
	}
	return store.ScoreContext{Attempt: 1}, nil
}

func (f *fakeScoreStore) ReviewFiles(_ context.Context, ref store.ReviewRef) ([]score.FileStat, error) {
	return f.files[ref.Number], nil
}

func (f *fakeScoreStore) SetReviewScoring(_ context.Context, ref store.ReviewRef, diff store.DiffStats, rec store.ScoreRecord) error {
	if f.measured == nil {
		f.measured = map[int]store.DiffStats{}
	}
	f.measured[ref.Number] = diff
	return f.SetReviewScore(context.Background(), ref, rec)
}

func (f *fakeScoreStore) SetReviewScore(_ context.Context, ref store.ReviewRef, rec store.ScoreRecord) error {
	if ref.Number == f.failOn {
		return errors.New("write failed")
	}
	if f.written == nil {
		f.written = map[int]store.ScoreRecord{}
	}
	f.written[ref.Number] = rec
	return nil
}

func scoredReview(number int, verdict string, adds, dels int, head, diffSHA string) store.Review {
	return store.Review{
		Repo: "o/r", Number: number, Author: "alice", HeadSHA: head, Verdict: verdict,
		ReviewedAt: time.Now().Add(-time.Hour),
		Diff:       store.DiffStats{ScoredAdditions: adds, ScoredDeletions: dels, DiffSHA: diffSHA},
	}
}

func runRecompute(t *testing.T, fs *fakeScoreStore, dryRun bool) {
	t.Helper()
	if err := recompute(context.Background(), fs, config.Config{}, store.ScoreQuery{Repo: "o/r"}, dryRun); err != nil {
		t.Fatalf("recompute: %v", err)
	}
}

func TestRecomputeWritesDerivedScores(t *testing.T) {
	fs := &fakeScoreStore{rows: []store.Review{
		scoredReview(1, store.VerdictApproved, 40, 10, "sha", "sha"),
	}}
	runRecompute(t, fs, false)

	got, ok := fs.written[1]
	if !ok {
		t.Fatal("nothing written")
	}
	if got.Points() != 139 {
		t.Errorf("score = %d, want 139", got.Points())
	}
	if got.Source != store.ScoreDerived {
		t.Errorf("source = %q, want derived", got.Source)
	}
	// 40 added and 10 removed is 55 churn once removals weigh 1.5, which is
	// "medium" and not the "small" it read as when they weighed 0.5.
	if got.Bucket != "medium" {
		t.Errorf("bucket = %q, want medium recorded alongside the score", got.Bucket)
	}
}

// --dry-run has to be genuinely inert: it is the thing to reach for before
// moving points somebody already has.
func TestRecomputeDryRunWritesNothing(t *testing.T) {
	fs := &fakeScoreStore{rows: []store.Review{
		scoredReview(1, store.VerdictApproved, 40, 10, "sha", "sha"),
	}}
	runRecompute(t, fs, true)

	if len(fs.written) != 0 {
		t.Errorf("dry run wrote %d rows, want none", len(fs.written))
	}
}

// THE regression. The scheduler declines a review whose head moved mid-run but
// still records its diff, and those rows are exactly what --missing selects.
// recompute must decline them too rather than score them off a diff describing
// code the review never saw.
func TestRecomputeSkipsAStaleDiff(t *testing.T) {
	fs := &fakeScoreStore{rows: []store.Review{
		scoredReview(1, store.VerdictApproved, 40, 10, "sha-reviewed", "sha-newer"),
		scoredReview(2, store.VerdictApproved, 40, 10, "sha-ok", "sha-ok"),
	}}
	runRecompute(t, fs, false)

	if _, wrote := fs.written[1]; wrote {
		t.Error("a row whose diff describes another revision must not be scored")
	}
	if _, wrote := fs.written[2]; !wrote {
		t.Error("the healthy row should still be scored")
	}
}

// A discussion re-review is a second verdict at the same commit: no new code,
// so nothing to pay for.
func TestRecomputeZeroesADiscussionRereview(t *testing.T) {
	fs := &fakeScoreStore{
		rows: []store.Review{scoredReview(1, store.VerdictApproved, 40, 10, "sha", "sha")},
		sc:   map[int]store.ScoreContext{1: {Attempt: 1, ReviewedAtThisHead: true}},
	}
	runRecompute(t, fs, false)

	if got := fs.written[1]; got.Points() != 0 {
		t.Errorf("score = %d, want 0", got.Points())
	}
}

func TestRecomputePropagatesAWriteError(t *testing.T) {
	fs := &fakeScoreStore{
		rows:   []store.Review{scoredReview(1, store.VerdictApproved, 40, 10, "sha", "sha")},
		failOn: 1,
	}
	err := recompute(context.Background(), fs, config.Config{}, store.ScoreQuery{Repo: "o/r"}, false)
	if err == nil {
		t.Fatal("a failed write must surface, not be swallowed")
	}
}

// The blanket-refusal guard: rescoring all of history without saying so is the
// "everybody's points moved and nobody asked" case.
func TestRecomputeRefusesAnUnnarrowedSweep(t *testing.T) {
	cmd := scoreRecomputeCmd()
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err == nil {
		t.Error("recompute with no filter and no --all must refuse")
	}
}

// Changing the exclusion policy must be re-appliable from the stored
// measurement, with no network: that is the whole reason the per-file detail
// is kept.
func TestRecomputeReappliesExclusionsOffline(t *testing.T) {
	fs := &fakeScoreStore{
		rows: []store.Review{scoredReview(1, store.VerdictApproved, 8040, 2010, "sha", "sha")},
		files: map[int][]score.FileStat{1: {
			{Path: "main.go", Additions: 40, Deletions: 10},
			{Path: "package-lock.json", Additions: 8000, Deletions: 2000, Generated: true},
		}},
	}
	// Default policy honours the repo's declaration, so the lockfile drops out.
	runRecompute(t, fs, false)

	got := fs.measured[1]
	if got.ScoredAdditions != 40 || got.ScoredDeletions != 10 {
		t.Errorf("re-measured counts = +%d/-%d, want +40/-10", got.ScoredAdditions, got.ScoredDeletions)
	}
	if got.ExcludedFiles != 1 {
		t.Errorf("excluded = %d, want 1", got.ExcludedFiles)
	}
	// medium bucket, not huge: the raw 8040 never reaches the score.
	if fs.written[1].Bucket != "medium" {
		t.Errorf("bucket = %q, want medium", fs.written[1].Bucket)
	}
}

// A row with no stored measurement is rescored from the counts it already has,
// and says so, because nothing here can tell an arithmetic tweak from an
// exclusion change and silently assuming either would be wrong.
func TestRecomputeReportsWhenItCouldNotRemeasure(t *testing.T) {
	fs := &fakeScoreStore{rows: []store.Review{
		scoredReview(1, store.VerdictApproved, 40, 10, "sha", "sha"),
	}}
	runRecompute(t, fs, false)

	if _, wrote := fs.written[1]; !wrote {
		t.Fatal("the row should still be rescored from its stored counts")
	}
	if got := fs.measured[1]; got.ScoredAdditions != 40 {
		t.Errorf("stored counts should be kept as-is, got %+v", got)
	}
}
