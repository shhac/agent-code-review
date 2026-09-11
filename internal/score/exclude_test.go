package score

import "testing"

var prFiles = []FileStat{
	{Path: "internal/score/score.go", Additions: 40, Deletions: 10},
	{Path: "package-lock.json", Additions: 8000, Deletions: 2000},
	{Path: "internal/dashboard/assets/index-abc.js", Additions: 12000, Deletions: 11000},
	{Path: "README.md", Additions: 3, Deletions: 1},
}

func TestApplyWithNoExclusions(t *testing.T) {
	got := NewExclusions(ParseAttrs(nil), nil).Apply(prFiles)
	want := Totals{Additions: 20043, Deletions: 13011, ExcludedFiles: 0}
	if got != want {
		t.Errorf("Apply() = %+v, want %+v", got, want)
	}
}

// The case that motivated the whole exclusion path: a 43-line change buried
// under a lockfile and a committed build bundle.
func TestApplyWithGitattributes(t *testing.T) {
	a := ParseAttrs(map[string]string{
		"": "package-lock.json  linguist-generated\ninternal/dashboard/assets/**  linguist-generated\n",
	})
	got := NewExclusions(a, nil).Apply(prFiles)
	want := Totals{Additions: 43, Deletions: 11, ExcludedFiles: 2}
	if got != want {
		t.Errorf("Apply() = %+v, want %+v", got, want)
	}

	// And the score it produces is the whole point: small, not huge.
	res := Compute(DefaultRules(), Input{
		Additions: got.Additions, Deletions: got.Deletions, Verdict: verdictApproved, Attempt: 1,
	})
	if res.Bucket != "small" {
		t.Errorf("bucket = %q, want small: exclusions should rescue this PR from huge", res.Bucket)
	}
}

// The operator's escape hatch, for repos that have not adopted the convention.
func TestApplyWithConfigGlobs(t *testing.T) {
	got := NewExclusions(ParseAttrs(nil), []string{"package-lock.json", "internal/dashboard/assets/**"}).Apply(prFiles)
	want := Totals{Additions: 43, Deletions: 11, ExcludedFiles: 2}
	if got != want {
		t.Errorf("Apply() = %+v, want %+v", got, want)
	}
}

func TestExcludesCombinesBothSources(t *testing.T) {
	e := NewExclusions(
		ParseAttrs(map[string]string{"": "*.lock  linguist-generated\n"}),
		[]string{"generated/**"},
	)
	for _, p := range []string{"Cargo.lock", "generated/api.ts"} {
		if !e.Excludes(p) {
			t.Errorf("Excludes(%q) = false, want true", p)
		}
	}
	if e.Excludes("main.go") {
		t.Error("main.go should not be excluded")
	}
}

// One unparseable glob in config must not take the rest down with it.
func TestBadGlobIsDroppedNotFatal(t *testing.T) {
	e := NewExclusions(ParseAttrs(nil), []string{"[unclosed", "good/**"})
	if !e.Excludes("good/x.go") {
		t.Error("the valid glob should still apply")
	}
}

func TestApplyEmptyFileList(t *testing.T) {
	if got := (NewExclusions(ParseAttrs(nil), nil).Apply(nil)); got != (Totals{}) {
		t.Errorf("Apply(nil) = %+v, want zero", got)
	}
}

// A PR whose every file is generated is a real thing (a lockfile bump, a
// regenerated client) and it must be worth NOTHING.
//
// This is the exclusion path's own worst failure mode: strip the generated
// lines and what is left is churn 0, which lands in the smallest bucket and
// has a net of 0, so it would collect the shrink bonus too and score 120 --
// more than a genuine +200/-100 PR. Exclusion exists so generated lines cannot
// bury a real change, not so a PR containing only generated lines can mint
// points.
func TestFullyGeneratedPRScoresNothing(t *testing.T) {
	e := NewExclusions(ParseAttrs(map[string]string{"": "**  linguist-generated\n"}), nil)
	got := e.Apply(prFiles)
	if got.Additions != 0 || got.Deletions != 0 || got.ExcludedFiles != 4 {
		t.Fatalf("Apply() = %+v, want everything excluded", got)
	}

	res := Compute(DefaultRules(), Input{
		Additions: got.Additions, Deletions: got.Deletions, Verdict: verdictApproved, Attempt: 1,
	})
	if res.Score != 0 {
		t.Errorf("a fully-generated PR scored %d, want 0", res.Score)
	}

	real := Compute(DefaultRules(), Input{Additions: 200, Deletions: 100, Verdict: verdictApproved, Attempt: 1})
	if res.Score >= real.Score {
		t.Errorf("a fully-generated PR (%d) must not beat a real one (%d)", res.Score, real.Score)
	}
}
