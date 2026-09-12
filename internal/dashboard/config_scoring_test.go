package dashboard

import (
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/score"
)

// The Config page draws the size ladder as a curve, so the response has to
// carry both halves of it: which curve is in force, and the control points it
// passes through.
//
// This also pins the invariant that removed a defensive helper here. The
// response used to map an empty curve onto a name, which could never happen:
// a resolved ruleset always starts from DefaultRules. Asserting that a bare
// config reports "linear" says the same thing where it can actually fail.
func TestScoringRespNamesTheCurveAndItsAnchors(t *testing.T) {
	got := scoringResp(config.Config{})
	if got.Curve != score.CurveLinear {
		t.Errorf("curve = %q, want the shipped %q rather than a blank", got.Curve, score.CurveLinear)
	}
	want := score.DefaultRules().Anchors()
	if len(got.Anchors) != len(want) {
		t.Fatalf("anchors = %v, want %v", got.Anchors, want)
	}
	for i := range want {
		if got.Anchors[i] != want[i] {
			t.Errorf("anchor %d = %v, want %v", i, got.Anchors[i], want[i])
		}
	}
	// The derived tail is the part the browser cannot work out for itself.
	// What it should BE is pinned in score; that it survives the wire is this
	// test's business.
	if last := got.Anchors[len(got.Anchors)-1]; last.Churn < 4000 || last.Multiplier != 0.2 {
		t.Errorf("tail anchor = %+v, want the derived one score computes", last)
	}
}

// A repo-less override still reaches the page: the Config view reports the
// global policy, which is what an operator edits.
func TestScoringRespCarriesAConfiguredCurve(t *testing.T) {
	cfg := config.Config{Scoring: config.ScoringSettings{Curve: score.CurveStep}}
	if got := scoringResp(cfg).Curve; got != score.CurveStep {
		t.Errorf("curve = %q, want %q", got, score.CurveStep)
	}
}

// The page shows ONE ruleset, and a repo with its own is not described by it.
// Naming those repos is what stops the ladder and the calculator from
// presenting the global policy as everybody's.
func TestScoringRespNamesTheReposItDoesNotDescribe(t *testing.T) {
	base := 50.0
	cfg := config.Config{Scoring: config.ScoringSettings{
		Repos: map[string]config.ScoringSettings{
			"o/second": {Base: &base},
			"o/first":  {Base: &base},
		},
	}}
	got := scoringResp(cfg).ScopedRepos
	if len(got) != 2 || got[0] != "o/first" || got[1] != "o/second" {
		t.Errorf("scoped repos = %v, want both, sorted", got)
	}
	if other := scoringResp(config.Config{}).ScopedRepos; len(other) != 0 {
		t.Errorf("scoped repos = %v, want none when nothing is narrowed", other)
	}
}
