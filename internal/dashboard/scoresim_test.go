package dashboard

import (
	"net/http"
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/score"
	"github.com/shhac/agent-code-review/internal/store"
)

func simulateReq(t *testing.T, body string) (int, scoreSimResp) {
	t.Helper()
	return serveJSON[scoreSimResp](t, testServer(withConfig(config.Config{})).handleScoreSimulate,
		http.MethodPost, "/api/score/simulate", body)
}

// The map has to be the scorer's own answer at every point, or an operator
// tunes against a picture of a policy the daemon does not run.
func TestSimulateGridIsTheScorersOwnAnswer(t *testing.T) {
	code, resp := simulateReq(t, `{"scoring":{},"range":400,"cells":8}`)
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	if len(resp.Grids) != 2 {
		t.Fatalf("grids = %d, want one per round count", len(resp.Grids))
	}
	rules := score.DefaultRules()
	for _, g := range resp.Grids {
		if len(g.Cells) != 8 || len(g.Cells[0]) != 8 {
			t.Fatalf("grid is %dx%d, want 8x8", len(g.Cells), len(g.Cells[0]))
		}
		for y := range g.Cells {
			for x := range g.Cells[y] {
				adds := int(float64(x)*g.Step + g.Step/2)
				dels := int(float64(y)*g.Step + g.Step/2)
				if got, want := g.Cells[y][x], roundsTotal(rules, adds, dels, g.Rounds); got != want {
					t.Fatalf("grid[%d][%d] at %d rounds = %d, want %d", y, x, g.Rounds, got, want)
				}
			}
		}
	}
}

// The three balanced PRs the defaults were tuned against. They answer the
// question the whole page is for, so a policy that inverts them should be
// visible without reading the map.
func TestSimulateAnswersTheTighterSolveQuestion(t *testing.T) {
	_, resp := simulateReq(t, `{"scoring":{},"range":400,"cells":8}`)
	want := []scoreSimProbe{{100, 100}, {200, 86}, {300, 71}}
	if len(resp.Probes) != len(want) {
		t.Fatalf("probes = %+v", resp.Probes)
	}
	for i := range want {
		if resp.Probes[i] != want[i] {
			t.Errorf("probe %d = %+v, want %+v", i, resp.Probes[i], want[i])
		}
	}
	// The measured best must agree with the landmark the dials name, to within
	// the plateau that whole-point rounding puts around it. A policy whose
	// stated peak and measured optimum disagree is one the page explains
	// wrongly.
	if changed := float64(resp.BestPR.Lines * 2); changed > resp.Peak || changed < resp.Peak*0.85 {
		t.Errorf("best balanced PR is %v changed lines, want it just under the stated peak of %v", changed, resp.Peak)
	}
	if resp.BestPR.Score != 100 {
		t.Errorf("best balanced PR scored %d, want size_points", resp.BestPR.Score)
	}
}

// Splitting a big change pays, and the piece size it pays for is the one the
// policy names. Both halves matter: an operator needs to know the premium
// before they ship a policy, and a premium that points at one-line PRs is a
// different policy from the one they think they picked.
func TestSimulateReportsWhatFragmentingPays(t *testing.T) {
	_, resp := simulateReq(t, `{"scoring":{},"range":400,"cells":8}`)
	if resp.Fragment.Gain <= 1 {
		t.Errorf("fragment = %+v, want splitting a big change to pay", resp.Fragment)
	}
	// And the size it points at is a real pull request rather than one line,
	// which is the whole reason the reward is quadratic near zero.
	if resp.Fragment.Lines != 50 {
		t.Errorf("fragment lines = %v, want the configured piece size", resp.Fragment.Lines)
	}
	// Moving the dial moves the answer, so the number is being derived from
	// the policy rather than remembered.
	_, wider := simulateReq(t, `{"scoring":{"piece_lines":100},"range":400,"cells":8}`)
	if wider.Fragment.Lines != 100 {
		t.Errorf("fragment lines = %v, want the reconfigured piece size", wider.Fragment.Lines)
	}
}

// A ruleset that cannot be used must be reported, not quietly replaced by the
// defaults: the operator would be shown the defaults' map while editing
// something else, which is the one failure a tuning tool must not have.
func TestSimulateRefusesAnInvalidRuleset(t *testing.T) {
	code, _ := simulateReq(t, `{"scoring":{"attempt_decay":1}}`)
	if code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400 for a decay that makes comment rounds outscore a first-pass approval", code)
	}
	if code, _ := simulateReq(t, `not json`); code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400 for a body that is not a scoring document", code)
	}
}

func TestSimulateIsPostOnly(t *testing.T) {
	code, _ := serveJSON[scoreSimResp](t, testServer(withConfig(config.Config{})).handleScoreSimulate,
		http.MethodGet, "/api/score/simulate", "")
	if code != http.StatusMethodNotAllowed {
		t.Errorf("code = %d, want 405", code)
	}
}

// The grid is the only thing here that costs anything, and the request says
// how big it is.
func TestSimulateBoundsTheGrid(t *testing.T) {
	_, resp := simulateReq(t, `{"scoring":{},"range":999999,"cells":9999}`)
	if n := len(resp.Grids[0].Cells); n != maxSimCells {
		t.Errorf("cells = %d, want the ceiling of %d", n, maxSimCells)
	}
	_, zero := simulateReq(t, `{"scoring":{}}`)
	if n := len(zero.Grids[0].Cells); n != defaultSimCells {
		t.Errorf("cells = %d, want the default %d", n, defaultSimCells)
	}
}

// Rounds compose the same way here as in the single-PR calculator: comment
// rounds, then an approval, each decayed.
func TestSimulateRoundsMatchTheCalculator(t *testing.T) {
	rules := score.DefaultRules()
	one := score.Compute(rules, score.Input{Additions: 40, Deletions: 10, Verdict: store.VerdictApproved, Attempt: 1}).Score
	two := score.Compute(rules, score.Input{Additions: 40, Deletions: 10, Verdict: store.VerdictCommented, Attempt: 1}).Score +
		score.Compute(rules, score.Input{Additions: 40, Deletions: 10, Verdict: store.VerdictApproved, Attempt: 2}).Score
	if got := roundsTotal(rules, 40, 10, 1); got != one {
		t.Errorf("one round = %d, want %d", got, one)
	}
	if got := roundsTotal(rules, 40, 10, 2); got != two {
		t.Errorf("two rounds = %d, want %d", got, two)
	}
	if two >= one {
		t.Errorf("two rounds scored %d against one round's %d: getting it right first time must pay more", two, one)
	}
}
