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
	if last := got.Anchors[len(got.Anchors)-1]; last.Churn != 4000 {
		t.Errorf("tail anchor at churn %v, want the derived 4000", last.Churn)
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
