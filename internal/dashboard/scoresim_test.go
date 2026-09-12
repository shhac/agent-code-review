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
	want := []scoreSimProbe{{100, 190}, {200, 158}, {300, 135}}
	if len(resp.Probes) != len(want) {
		t.Fatalf("probes = %+v", resp.Probes)
	}
	for i := range want {
		if resp.Probes[i] != want[i] {
			t.Errorf("probe %d = %+v, want %+v", i, resp.Probes[i], want[i])
		}
	}
	if resp.Peak.Lines > 60 {
		t.Errorf("peak = %+v, want the best-paid PR to be a small one", resp.Peak)
	}
}

// The cost of a sub-1 exponent, surfaced rather than buried: chopping work up
// pays, and how much is the number an operator needs before they ship a
// policy. The scan must look BELOW one unit of churn, which is where the
// cheapest possible PR lives.
func TestSimulateReportsWhatFragmentingPays(t *testing.T) {
	_, resp := simulateReq(t, `{"scoring":{},"range":400,"cells":8}`)
	if resp.Fragment.Gain <= 1 {
		t.Errorf("fragment = %+v, want the shipped policy to admit that splitting pays", resp.Fragment)
	}
	if resp.Fragment.Lines != 1 {
		t.Errorf("fragment lines = %v, want the one-line optimum the shipped ladder leaves open", resp.Fragment.Lines)
	}

	// And the dial that closes it: a first tier paying nothing moves the
	// optimum back to a real pull request.
	_, floored := simulateReq(t, `{"scoring":{"buckets":[
		{"name":"tiny","max_churn":10,"multiplier":0},
		{"name":"small","max_churn":50,"multiplier":1.5},
		{"name":"huge","multiplier":0.2}]},"range":400,"cells":8}`)
	if floored.Fragment.Lines < 10 {
		t.Errorf("floored fragment lines = %v, want the optimum pushed past the zero-rated tier", floored.Fragment.Lines)
	}
	// A tier that pays nothing makes the spread unbounded. It travels as null
	// rather than as an infinity, which encoding/json cannot write: the reply
	// would arrive empty, with a 200 on it.
	if floored.RateSpread != nil {
		t.Errorf("rate spread = %v, want null when a tier pays nothing", *floored.RateSpread)
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
