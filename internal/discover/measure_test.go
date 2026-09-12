package discover

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/score"
	"github.com/shhac/agent-code-review/internal/store"
)

func TestMeasureFetchFailureLeavesSizeUnknownRatherThanZero(t *testing.T) {
	failure := errors.New("rate limited")
	m := Measurer{
		Diff: func(context.Context, string, int) (PRDiff, error) {
			return PRDiff{}, failure
		},
		Attrs: func(context.Context, string, string, []score.FileStat) (map[string]string, error) {
			t.Fatal("a failed diff must not fetch declarations")
			return nil, nil
		},
	}
	got, err := m.Measure(context.Background(), score.DefaultRules(), "o/r", 1)
	if !errors.Is(err, failure) {
		t.Fatalf("error = %v, want the fetch failure", err)
	}
	if got.Stats.Recorded() || !reflect.DeepEqual(got, Measurement{}) {
		t.Fatalf("failed fetch left scoreable evidence: %+v", got)
	}
}

func TestMeasureTruncationCannotTurnPartialFilesIntoCompleteEvidence(t *testing.T) {
	m := Measurer{
		Diff: func(context.Context, string, int) (PRDiff, error) {
			return PRDiff{
				HeadSHA: "head", Additions: 8000, Deletions: 2000, ChangedFiles: 4000, Truncated: true,
				Files: []score.FileStat{{Path: "first.go", Additions: 40, Deletions: 10}},
			}, nil
		},
		Attrs: func(context.Context, string, string, []score.FileStat) (map[string]string, error) {
			t.Fatal("a partial listing cannot support attribute exclusions")
			return nil, nil
		},
	}
	// Excluding the listed files would turn the unseen remainder into zero.
	rules := config.Config{Scoring: config.ScoringSettings{ExcludePaths: []string{"**"}}}.ResolveScoring("o/r")
	got, err := m.Measure(context.Background(), rules, "o/r", 1)
	if err != nil {
		t.Fatal(err)
	}
	want := store.DiffStats{
		Additions: 8000, Deletions: 2000, ChangedFiles: 4000,
		ScoredAdditions: 8000, ScoredDeletions: 2000, DiffSHA: "head",
	}
	if got.Stats != want || len(got.Files) != 0 {
		t.Fatalf("partial listing must retain only complete raw totals: %+v", got)
	}
	if len(got.Notes) != 1 || !strings.Contains(got.Notes[0], "truncated") {
		t.Errorf("degraded measurement was not explained: %v", got.Notes)
	}
}

func TestMeasureUnreadableAttributesStillHonoursOperatorExclusions(t *testing.T) {
	m := Measurer{
		Diff: func(context.Context, string, int) (PRDiff, error) {
			return PRDiff{
				HeadSHA: "head", Additions: 140, Deletions: 60, ChangedFiles: 2,
				Files: []score.FileStat{
					{Path: "main.go", Additions: 40, Deletions: 10},
					{Path: "deps.lock", Additions: 100, Deletions: 50},
				},
			}, nil
		},
		Attrs: func(context.Context, string, string, []score.FileStat) (map[string]string, error) {
			return nil, errors.New("unavailable")
		},
	}
	rules := config.Config{Scoring: config.ScoringSettings{ExcludePaths: []string{"*.lock"}}}.ResolveScoring("o/r")
	got, err := m.Measure(context.Background(), rules, "o/r", 1)
	if err != nil {
		t.Fatal(err)
	}
	want := store.DiffStats{
		Additions: 140, Deletions: 60, ChangedFiles: 2, ScoredAdditions: 40,
		ScoredDeletions: 10, ExcludedFiles: 1, DiffSHA: "head",
	}
	if got.Stats != want || len(got.Files) != 2 {
		t.Fatalf("attribute failure lost the measurement or operator policy: %+v", got)
	}
	for _, file := range got.Files {
		if file.Generated {
			t.Errorf("unreadable declarations invented a generated mark for %s", file.Path)
		}
	}
	if len(got.Notes) != 1 || !strings.Contains(got.Notes[0], "unavailable") {
		t.Errorf("attribute failure was not explained: %v", got.Notes)
	}
}

func TestMeasureDisabledAttributesDoesNotFetchOrExcludeDeclarations(t *testing.T) {
	m := Measurer{
		Diff: func(context.Context, string, int) (PRDiff, error) {
			return PRDiff{
				HeadSHA: "head", Additions: 40, Deletions: 10, ChangedFiles: 1,
				Files: []score.FileStat{{Path: "generated.go", Additions: 40, Deletions: 10}},
			}, nil
		},
		Attrs: func(context.Context, string, string, []score.FileStat) (map[string]string, error) {
			t.Fatal("disabled attributes must not cost a GitHub call")
			return map[string]string{"": "* linguist-generated"}, nil
		},
	}
	disabled := false
	rules := config.Config{Scoring: config.ScoringSettings{UseGitattributes: &disabled}}.ResolveScoring("o/r")
	got, err := m.Measure(context.Background(), rules, "o/r", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stats.ScoredAdditions != 40 || got.Stats.ScoredDeletions != 10 || got.Stats.ExcludedFiles != 0 {
		t.Fatalf("disabled declarations changed the scored counts: %+v", got)
	}
	if len(got.Files) != 1 || got.Files[0].Generated || len(got.Notes) != 0 {
		t.Fatalf("disabled declarations are not a failed or generated measurement: %+v", got)
	}
}

func TestMeasureKeepsRepositoryMarksSeparateFromOperatorExclusions(t *testing.T) {
	m := Measurer{
		Diff: func(_ context.Context, repo string, number int) (PRDiff, error) {
			if repo != "o/r" || number != 17 {
				t.Fatalf("measured %s#%d, want o/r#17", repo, number)
			}
			return PRDiff{
				HeadSHA: "head", Additions: 8140, Deletions: 2060, ChangedFiles: 3,
				Files: []score.FileStat{
					{Path: "main.go", Additions: 40, Deletions: 10},
					{Path: "generated.go", Additions: 8000, Deletions: 2000},
					{Path: "deps.lock", Additions: 100, Deletions: 50},
				},
			}, nil
		},
		Attrs: func(_ context.Context, repo, _ string, files []score.FileStat) (map[string]string, error) {
			if repo != "o/r" || len(files) != 3 {
				t.Fatalf("declarations fetched for the wrong measurement: %s %+v", repo, files)
			}
			return map[string]string{"": "generated.go linguist-generated"}, nil
		},
	}
	rules := config.Config{Scoring: config.ScoringSettings{ExcludePaths: []string{"*.lock"}}}.ResolveScoring("o/r")
	got, err := m.Measure(context.Background(), rules, "o/r", 17)
	if err != nil {
		t.Fatal(err)
	}
	want := store.DiffStats{
		Additions: 8140, Deletions: 2060, ChangedFiles: 3, ScoredAdditions: 40,
		ScoredDeletions: 10, ExcludedFiles: 2, DiffSHA: "head",
	}
	if got.Stats != want || len(got.Notes) != 0 {
		t.Fatalf("measurement = %+v, want %+v without degradation", got, want)
	}
	wantFiles := []score.FileStat{
		{Path: "main.go", Additions: 40, Deletions: 10},
		{Path: "generated.go", Additions: 8000, Deletions: 2000, Generated: true},
		{Path: "deps.lock", Additions: 100, Deletions: 50},
	}
	if !reflect.DeepEqual(got.Files, wantFiles) {
		t.Fatalf("file evidence = %+v, want %+v", got.Files, wantFiles)
	}
	// Removing the operator's glob must restore the lockfile, without also
	// restoring files the repository declared generated.
	recount := score.Recount(got.Files, score.DefaultRules())
	if recount != (score.Totals{Additions: 140, Deletions: 60, ExcludedFiles: 1}) {
		t.Errorf("stored evidence cannot support an independent policy change: %+v", recount)
	}
}

// An unusable scoring block must not measure under its own exclusions and then
// record the defaults' hash over the result.
//
// ResolveScoring falls back to the shipped defaults when a ruleset cannot be
// used, which is right at review time: a bad multiplier must not wedge the
// reviewer. Measuring from the RAW settings meanwhile let the two disagree.
// size_points 0 is invalid, so this config scores under the defaults, while
// its exclude_paths would have thrown every line away. A row measured that way
// looks current to `score ls --stale` forever, because the hash beside it
// belongs to a policy that never touched it.
func TestAnInvalidRulesetMeasuresUnderTheRulesItWillBeScoredBy(t *testing.T) {
	zero := 0.0
	cfg := config.Config{Scoring: config.ScoringSettings{
		SizePoints:   &zero,
		ExcludePaths: []string{"**"},
	}}
	rules := cfg.ResolveScoring("o/r")
	if err := rules.Validate(); err != nil {
		t.Fatalf("the fallback ruleset should be usable: %v", err)
	}

	m := Measurer{
		Diff: func(context.Context, string, int) (PRDiff, error) {
			return PRDiff{
				HeadSHA: "sha", Additions: 40, Deletions: 10, ChangedFiles: 1,
				Files: []score.FileStat{{Path: "main.go", Additions: 40, Deletions: 10}},
			}, nil
		},
		Attrs: func(context.Context, string, string, []score.FileStat) (map[string]string, error) {
			return nil, nil
		},
	}
	got, err := m.Measure(context.Background(), rules, "o/r", 1)
	if err != nil {
		t.Fatal(err)
	}
	// The defaults exclude nothing, so the lines survive. Under the raw
	// settings the "**" glob would have taken all of them and the row would
	// have scored zero under a hash that says otherwise.
	if got.Stats.ScoredAdditions != 40 || got.Stats.ExcludedFiles != 0 {
		t.Errorf("measured %+v, want the fallback ruleset's exclusions (none) rather than the unusable block's",
			got.Stats)
	}
}
