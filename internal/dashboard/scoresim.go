package dashboard

// "What would this policy pay?", answered for a whole ruleset at once.
//
// The Config page lets an operator tune a scoring policy against a map of what
// every PR shape would earn under it. That map is computed HERE, by the same
// score.Compute a real review goes through, for the same reason the single-PR
// preview is: every dial is already on the page, so drawing the map in the
// browser would mean a second implementation of the whole policy, and a
// tuning tool that disagrees with the scorer is worse than none. The browser
// gets numbers and paints them.

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/score"
	"github.com/shhac/agent-code-review/internal/store"
)

// Bounds on one simulation. The grid is the only thing here that costs
// anything, and it costs cells x cells x rounds calls to a pure function.
const (
	defaultSimCells = 48
	maxSimCells     = 96
	defaultSimRange = 400
	maxSimRange     = 20000
)

// simProbes are the balanced pull requests the page asks about by name: the
// same solve written at three sizes. They are the question the whole exponent
// exists to answer, so they travel with every simulation.
var simProbes = []int{100, 200, 300}

type scoreSimReq struct {
	Scoring config.ScoringSettings `json:"scoring"`
	Range   int                    `json:"range"`
	Cells   int                    `json:"cells"`
}

// scoreSimResp is one policy, surveyed.
type scoreSimResp struct {
	Anchors []score.Anchor `json:"anchors"`
	// Grids are the score at each point of a lines-added by lines-removed
	// grid, one per number of review rounds. Row 0 is the fewest removed
	// lines; the browser flips it to put removals up the page.
	Grids []scoreSimGrid `json:"grids"`
	// Max is the largest score in ANY grid, so the two maps can share a colour
	// scale and be read against each other.
	Max    int             `json:"max"`
	Probes []scoreSimProbe `json:"probes"`
	// Peak is the best-paid balanced PR: the shape this policy is asking for.
	Peak scoreSimProbe `json:"peak"`
	// Fragment is what the policy pays somebody who chops their work up:
	// the churn per piece that maximises points, and what 2000 lines earn
	// split that way against shipped whole.
	Fragment scoreSimFragment `json:"fragment"`
	// RateSpread is the best size-rate over the worst. Under a proportional
	// policy it is the entire ceiling on what any decomposition can gain;
	// below that it is only part of the story, which is what Fragment tells.
	//
	// Null when a tier pays nothing, because the ratio is then unbounded. A
	// pointer rather than an infinity: encoding/json refuses to marshal one,
	// and writeJSON has already sent its status line by the time the encoder
	// finds out, so the whole response would arrive empty with a 200 on it.
	RateSpread *float64 `json:"rate_spread"`
}

type scoreSimGrid struct {
	Rounds int     `json:"rounds"`
	Step   float64 `json:"step"`
	Cells  [][]int `json:"cells"`
}

type scoreSimProbe struct {
	Lines int `json:"lines"`
	Score int `json:"score"`
}

type scoreSimFragment struct {
	// Lines is the size of one piece, in lines: what somebody chopping their
	// work up would aim for. A real pull request, or a sign the policy has a
	// hole in it.
	Lines int     `json:"lines"`
	Whole int     `json:"whole"`
	Split int     `json:"split"`
	Gain  float64 `json:"gain"`
}

// handleScoreSimulate surveys a candidate ruleset.
//
// POST, and not because it writes anything: the request body IS a scoring
// document, which does not belong in a query string, and the reply is a few
// thousand numbers. Nothing here touches the store or the config on disk.
func (s *Server) handleScoreSimulate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req scoreSimReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "body must be a scoring document: "+err.Error())
		return
	}
	cfg := config.Config{Scoring: req.Scoring}
	// Reported rather than swallowed. ResolveScoring falls back to the shipped
	// defaults for an invalid ruleset, which is right at review time and
	// exactly wrong here: the operator would be shown the defaults' map while
	// editing something else.
	if problems := cfg.ValidateScoring(); len(problems) > 0 {
		httpError(w, http.StatusBadRequest, strings.Join(problems, "; "))
		return
	}
	writeJSON(w, http.StatusOK, simulate(cfg.ResolveScoring(""), req.Range, req.Cells))
}

func simulate(rules score.Rules, lineRange, cells int) scoreSimResp {
	if lineRange <= 0 {
		lineRange = defaultSimRange
	}
	if lineRange > maxSimRange {
		lineRange = maxSimRange
	}
	if cells <= 0 {
		cells = defaultSimCells
	}
	if cells > maxSimCells {
		cells = maxSimCells
	}

	resp := scoreSimResp{Anchors: rules.Anchors()}
	step := float64(lineRange) / float64(cells)
	for _, rounds := range []int{1, 2} {
		grid := scoreSimGrid{Rounds: rounds, Step: step, Cells: make([][]int, cells)}
		for y := 0; y < cells; y++ {
			row := make([]int, cells)
			dels := int(math.Round((float64(y) + 0.5) * step))
			for x := 0; x < cells; x++ {
				adds := int(math.Round((float64(x) + 0.5) * step))
				v := roundsTotal(rules, adds, dels, rounds)
				row[x] = v
				if v > resp.Max {
					resp.Max = v
				}
			}
			grid.Cells[y] = row
		}
		resp.Grids = append(resp.Grids, grid)
	}

	for _, n := range simProbes {
		resp.Probes = append(resp.Probes, scoreSimProbe{Lines: n, Score: roundsTotal(rules, n, n, 1)})
	}
	resp.Peak = peakBalanced(rules)
	resp.Fragment = fragmentPayoff(rules)
	resp.RateSpread = rateSpread(rules)
	return resp
}

// roundsTotal is a PR reviewed `rounds` times: comment rounds, then an
// approval. It matches what the single-PR calculator does with the same
// sequence, so the two halves of the page cannot disagree.
func roundsTotal(rules score.Rules, adds, dels, rounds int) int {
	sum := 0
	for i := 1; i < rounds; i++ {
		sum += score.Compute(rules, score.Input{
			Additions: adds, Deletions: dels, Verdict: store.VerdictCommented, Attempt: i,
		}).Score
	}
	return sum + score.Compute(rules, score.Input{
		Additions: adds, Deletions: dels, Verdict: store.VerdictApproved, Attempt: rounds,
	}).Score
}

// peakBalanced is the best-paid PR that neither grows nor shrinks the tree.
// The shape a policy pays most for is the shape it is asking for, and it is
// not always the one the operator thinks they configured.
func peakBalanced(rules score.Rules) scoreSimProbe {
	best := scoreSimProbe{}
	for n := 1; n <= 3000; n++ {
		if v := roundsTotal(rules, n, n, 1); v > best.Score {
			best = scoreSimProbe{Lines: n, Score: v}
		}
	}
	return best
}

// fragmentPayoff is what chopping work up is worth under this policy.
//
// Splitting a change of n lines into pieces of p lines earns (n/p) x score(p),
// so the piece size that maximises it is wherever points PER LINE peak, and
// that size is the thing worth reporting: a real pull request, or one line.
// Measured in lines rather than churn because lines are what somebody
// actually divides, and the two differ once a removal weighs more than an
// addition.
func fragmentPayoff(rules score.Rules) scoreSimFragment {
	const lines = 2000
	best, at := math.Inf(-1), 1
	for p := 1; p <= lines; p++ {
		per := float64(score.Compute(rules, score.Input{
			Additions: p, Verdict: store.VerdictApproved, Attempt: 1,
		}).Score) / float64(p)
		if per > best {
			best, at = per, p
		}
	}
	whole := score.Compute(rules, score.Input{Additions: lines, Verdict: store.VerdictApproved, Attempt: 1}).Score
	split := (lines / at) * score.Compute(rules, score.Input{
		Additions: at, Verdict: store.VerdictApproved, Attempt: 1,
	}).Score
	out := scoreSimFragment{Lines: at, Whole: whole, Split: split}
	if whole > 0 {
		out.Gain = float64(split) / float64(whole)
	}
	return out
}

// rateSpread is the best size-rate over the worst, across the range a real PR
// could land in. Nil when the worst rate is zero or below, where the ratio
// stops meaning anything.
func rateSpread(rules score.Rules) *float64 {
	lo, hi := math.Inf(1), math.Inf(-1)
	for c := 1; c <= 6000; c++ {
		r := rules.Rate(float64(c))
		lo, hi = math.Min(lo, r), math.Max(hi, r)
	}
	if lo <= 0 {
		return nil
	}
	spread := hi / lo
	return &spread
}
