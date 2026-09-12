package dashboard

import (
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/score"
)

// The Config page explains a score in terms of the policy's landmarks, and
// neither of them is configured: the peak and the tier boundaries are DERIVED
// from piece_lines and size_falloff. Sending them keeps the page from carrying
// its own copy of arithmetic that would then drift.
func TestScoringRespCarriesTheDerivedLandmarks(t *testing.T) {
	got := scoringResp(config.Config{})
	r := score.DefaultRules()

	if got.PieceLines != r.PieceLines || got.SizePoints != r.SizePoints ||
		got.SizeFalloff != r.SizeFalloff || got.RemovalPointsPer100 != r.RemovalPointsPer100 {
		t.Errorf("dials = %+v, want the shipped ones", got)
	}
	if got.Peak != r.Peak() || got.Peak != 200 {
		t.Errorf("peak = %v, want score's %v", got.Peak, r.Peak())
	}
	if len(got.Tiers) != 5 || got.Tiers[0].Name != "tiny" || got.Tiers[4].UpTo != 0 {
		t.Errorf("tiers = %+v, want five labels ending in an open-ended one", got.Tiers)
	}
	if got.Tiers[1].UpTo != r.PieceLines || got.Tiers[2].UpTo != r.Peak() {
		t.Errorf("tiers = %+v, want the boundaries to follow the dials", got.Tiers)
	}
}

// A configured dial reaches the page: the Config view reports the global
// policy, which is what an operator edits.
func TestScoringRespCarriesAConfiguredDial(t *testing.T) {
	lines := 120.0
	cfg := config.Config{Scoring: config.ScoringSettings{PieceLines: &lines}}
	got := scoringResp(cfg)
	if got.PieceLines != 120 {
		t.Errorf("piece_lines = %v, want the configured 120", got.PieceLines)
	}
	// And the landmarks move with it rather than staying on the defaults.
	if got.Peak != 480 || got.Tiers[1].UpTo != 120 {
		t.Errorf("peak = %v, tiers = %+v: the landmarks must follow the dial", got.Peak, got.Tiers)
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
