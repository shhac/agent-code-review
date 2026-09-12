package cli

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/discover"
	"github.com/shhac/agent-code-review/internal/score"
	"github.com/shhac/agent-code-review/internal/store"
)

func refetchMeasurer(diff discover.DiffFn) discover.Measurer {
	return discover.Measurer{
		Diff: diff,
		Attrs: func(context.Context, string, string, []score.FileStat) (map[string]string, error) {
			return nil, nil
		},
	}
}

func TestRefetchReplacesOldCountsAndUsesTheReposScoringPolicy(t *testing.T) {
	fs := &fakeScoreStore{rows: []store.Review{
		scoredReview(1, store.VerdictApproved, 8000, 2000, "head", "old-diff"),
	}}
	points := 200.0
	cfg := config.Config{Scoring: config.ScoringSettings{Repos: map[string]config.ScoringSettings{
		"o/r": {SizePoints: &points, ExcludePaths: []string{"*.lock"}},
	}}}
	m := refetchMeasurer(func(_ context.Context, repo string, number int) (discover.PRDiff, error) {
		if repo != "o/r" || number != 1 {
			t.Fatalf("fetch = %s#%d, want o/r#1", repo, number)
		}
		return discover.PRDiff{
			HeadSHA: "head", Additions: 140, Deletions: 60, ChangedFiles: 2,
			Files: []score.FileStat{
				{Path: "main.go", Additions: 40, Deletions: 10},
				{Path: "deps.lock", Additions: 100, Deletions: 50},
			},
		}, nil
	})
	if err := refetch(context.Background(), fs, cfg, m, store.ScoreQuery{Repo: "o/r"}, false); err != nil {
		t.Fatal(err)
	}
	want := store.DiffStats{
		Additions: 140, Deletions: 60, ChangedFiles: 2, ScoredAdditions: 40,
		ScoredDeletions: 10, ExcludedFiles: 1, DiffSHA: "head",
	}
	if got := fs.measured[1]; got != want {
		t.Errorf("repaired counts = %+v, want %+v", got, want)
	}
	got := fs.written[1]
	if got.Points() != 100 || got.Source != store.ScoreDerived || got.Rules != cfg.ResolveScoring("o/r").Hash() {
		t.Errorf("repaired score used old counts or the global policy: %+v", got)
	}
}

func TestRefetchSkipsUnusableEvidenceWithoutAbandoningLaterReviews(t *testing.T) {
	fs := &fakeScoreStore{rows: []store.Review{
		scoredReview(1, store.VerdictApproved, 0, 0, "head", ""),
		scoredReview(2, store.VerdictApproved, 0, 0, "12345678-old", ""),
		scoredReview(3, store.VerdictSkipped, 0, 0, "head", ""),
		scoredReview(4, store.VerdictApproved, 0, 0, "head", ""),
	}}
	var fetched []int
	m := refetchMeasurer(func(_ context.Context, _ string, number int) (discover.PRDiff, error) {
		fetched = append(fetched, number)
		if number == 1 {
			return discover.PRDiff{}, errors.New("rate limited")
		}
		head := "head"
		if number == 2 {
			// Matching display prefixes must not make different revisions equal.
			head = "12345678-new"
		}
		return discover.PRDiff{
			HeadSHA: head, Additions: 40, Deletions: 10, ChangedFiles: 1,
			Files: []score.FileStat{{Path: "main.go", Additions: 40, Deletions: 10}},
		}, nil
	})
	if err := refetch(context.Background(), fs, config.Config{}, m, store.ScoreQuery{}, false); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fetched, []int{1, 2, 3, 4}) {
		t.Errorf("sweep abandoned later rows: fetched %v", fetched)
	}
	if len(fs.written) != 1 || len(fs.measured) != 1 || fs.written[4].Points() != 50 {
		t.Errorf("only the healthy row should be repaired: scores=%+v measurements=%+v", fs.written, fs.measured)
	}
}

func TestRefetchDryRunMeasuresWithoutWritingTheLedger(t *testing.T) {
	fs := &fakeScoreStore{rows: []store.Review{
		scoredReview(1, store.VerdictApproved, 0, 0, "head", ""),
	}}
	fetched := false
	m := refetchMeasurer(func(context.Context, string, int) (discover.PRDiff, error) {
		fetched = true
		return discover.PRDiff{
			HeadSHA: "head", Additions: 40, Deletions: 10, ChangedFiles: 1,
			Files: []score.FileStat{{Path: "main.go", Additions: 40, Deletions: 10}},
		}, nil
	})
	if err := refetch(context.Background(), fs, config.Config{}, m, store.ScoreQuery{}, true); err != nil {
		t.Fatal(err)
	}
	if !fetched {
		t.Fatal("dry run skipped the measurement it should preview")
	}
	if len(fs.written) != 0 || len(fs.measured) != 0 {
		t.Errorf("dry run changed the ledger: scores=%+v measurements=%+v", fs.written, fs.measured)
	}
}

func TestRefetchStopsWhenTheLedgerWriteFails(t *testing.T) {
	fs := &fakeScoreStore{
		rows: []store.Review{
			scoredReview(1, store.VerdictApproved, 0, 0, "head", ""),
			scoredReview(2, store.VerdictApproved, 0, 0, "head", ""),
		},
		failOn: 1,
	}
	var fetched []int
	m := refetchMeasurer(func(_ context.Context, _ string, number int) (discover.PRDiff, error) {
		fetched = append(fetched, number)
		return discover.PRDiff{HeadSHA: "head"}, nil
	})
	err := refetch(context.Background(), fs, config.Config{}, m, store.ScoreQuery{}, false)
	if err == nil || err.Error() != "write failed" {
		t.Fatalf("write failure was swallowed: %v", err)
	}
	if !reflect.DeepEqual(fetched, []int{1}) || len(fs.written) != 0 {
		t.Errorf("sweep continued after a failed write: fetched=%v written=%+v", fetched, fs.written)
	}
}

func TestRefetchRefusesAnUnnarrowedSweep(t *testing.T) {
	cmd := scoreRefetchCmd()
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err == nil {
		t.Error("refetch with no filter and no --all must refuse before spending GitHub calls")
	}
}

// A repair must store the per-file detail it just paid an API call for.
//
// Without it the row comes back carrying totals and nothing else, so the NEXT
// change to the exclusion policy cannot be re-applied offline and has to spend
// the call again. That is the whole difference between recompute and refetch,
// and it was quietly lost because the write listed every column except this
// one.
func TestRefetchStoresTheEvidenceItPaidFor(t *testing.T) {
	fs := &fakeScoreStore{rows: []store.Review{
		scoredReview(1, store.VerdictApproved, 0, 0, "head", ""),
	}}
	files := []score.FileStat{
		{Path: "main.go", Additions: 40, Deletions: 10},
		{Path: "deps.lock", Additions: 100, Deletions: 50},
	}
	m := refetchMeasurer(func(context.Context, string, int) (discover.PRDiff, error) {
		return discover.PRDiff{HeadSHA: "head", Additions: 140, Deletions: 60, ChangedFiles: 2, Files: files}, nil
	})
	if err := refetch(context.Background(), fs, config.Config{}, m, store.ScoreQuery{Repo: "o/r"}, false); err != nil {
		t.Fatal(err)
	}
	got := fs.storedFiles[1]
	if len(got) != len(files) {
		t.Fatalf("stored %d files, want the %d it measured", len(got), len(files))
	}
	for i := range files {
		if got[i].Path != files[i].Path || got[i].Additions != files[i].Additions {
			t.Errorf("stored file %d = %+v, want %+v", i, got[i], files[i])
		}
	}
}

// A truncated listing measures without exclusions and stores no detail, so the
// repair must CLEAR whatever a previous measurement left. Detail from a
// complete listing sitting under counts that were not derived from it would
// let a later recount claim a precision this measurement never had.
func TestRefetchClearsEvidenceATruncatedListingCannotSupport(t *testing.T) {
	fs := &fakeScoreStore{
		rows:  []store.Review{scoredReview(1, store.VerdictApproved, 40, 10, "head", "head")},
		files: map[int][]score.FileStat{1: {{Path: "main.go", Additions: 40, Deletions: 10}}},
	}
	m := refetchMeasurer(func(context.Context, string, int) (discover.PRDiff, error) {
		return discover.PRDiff{HeadSHA: "head", Additions: 9000, Deletions: 500, ChangedFiles: 4000, Truncated: true}, nil
	})
	if err := refetch(context.Background(), fs, config.Config{}, m, store.ScoreQuery{Repo: "o/r"}, false); err != nil {
		t.Fatal(err)
	}
	if got := fs.storedFiles[1]; len(got) != 0 {
		t.Errorf("stored %d files from a truncated listing, want none", len(got))
	}
	if got := fs.measured[1]; got.ScoredAdditions != 9000 || got.ExcludedFiles != 0 {
		t.Errorf("measured %+v, want the raw totals with nothing excluded", got)
	}
}
