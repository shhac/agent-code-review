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
	// Peak and Tiers are where this policy's landmarks fall: the best a single
	// PR can do, and the labels a score is explained with. Derived from the
	// dials rather than set, so the page must not work them out itself.
	Peak  float64      `json:"peak"`
	Tiers []score.Tier `json:"tiers"`
	// Grids are the score at each point of a lines-added by lines-removed
	// grid, one per number of review rounds. Row 0 is the fewest removed
	// lines; the browser flips it to put removals up the page.
	Grids []scoreSimGrid `json:"grids"`
	// Max is the largest score in ANY grid, so the two maps can share a colour
	// scale and be read against each other.
	Max int `json:"max"`
	// Curve is the size reward sampled across the range, for drawing. Sampled
	// HERE because the shape is the policy: a browser that plotted its own
	// x^2/(1+x)^k would be a second implementation of it, and a chart that
	// disagrees with the scorer is worse than no chart.
	Curve  []scoreSimPoint `json:"curve"`
	Probes []scoreSimProbe `json:"probes"`
	// BestPR is the best-paid balanced PR found by scanning, which should land
	// on Peak: a policy whose stated landmark and measured optimum disagree is
	// one the page is explaining wrongly.
	BestPR scoreSimProbe `json:"best_pr"`
	// Fragment is what the policy pays somebody who chops their work up:
	// the churn per piece that maximises points, and what 2000 lines earn
	// split that way against shipped whole.
	Fragment scoreSimFragment `json:"fragment"`
}

type scoreSimGrid struct {
	Rounds int     `json:"rounds"`
	Step   float64 `json:"step"`
	Cells  [][]int `json:"cells"`
}

// scoreSimPoint is one sample of the size curve: what a PR of this many
// changed lines earns for its size, before any verdict or decay.
type scoreSimPoint struct {
	Changed float64 `json:"changed"`
	Points  float64 `json:"points"`
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

	resp := scoreSimResp{Peak: rules.Peak(), Tiers: rules.Tiers()}
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

	resp.Curve = sampleCurve(rules, lineRange)
	for _, n := range simProbes {
		resp.Probes = append(resp.Probes, scoreSimProbe{Lines: n, Score: roundsTotal(rules, n, n, 1)})
	}
	resp.BestPR = peakBalanced(rules)
	resp.Fragment = fragmentPayoff(rules)
	return resp
}

// sampleCurve walks the size reward from one changed line out past the peak.
//
// Logarithmic steps, because the interesting part is all at the small end: a
// linear walk across a 20,000-line axis spends every sample on PRs nobody
// should be writing and draws the peak as a spike two pixels wide.
func sampleCurve(rules score.Rules, lineRange int) []scoreSimPoint {
	const samples = 120
	hi := math.Max(float64(lineRange), rules.Peak()*4)
	out := make([]scoreSimPoint, 0, samples+1)
	for i := 0; i <= samples; i++ {
		changed := math.Exp(float64(i) / float64(samples) * math.Log(hi))
		out = append(out, scoreSimPoint{Changed: changed, Points: rules.SizeReward(changed)})
	}
	return out
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
//
// Scanned on the awarded (rounded) score, because this is what somebody would
// actually earn. Rounding makes a plateau around the real peak, so the answer
// is the SMALLEST PR that earns the maximum, which is the honest reading of
// "how big does this need to be".
func peakBalanced(rules score.Rules) scoreSimProbe {
	best := scoreSimProbe{}
	for n := 1; n <= 3000; n++ {
		if v := roundsTotal(rules, n, n, 1); v > best.Score {
			best = scoreSimProbe{Lines: n, Score: v}
		}
	}
	return best
}

// fragmentPayoff is what chopping work up is worth under this policy: the
// piece size somebody splitting a large change should aim for, and what that
// earns against shipping it whole.
//
// The optimum is found on the UNROUNDED reward. Rounding to whole points
// creates ties across a band of piece sizes either side of the real peak, and
// an argmax over ties reports whichever came first: with the shipped dials
// that is 42 lines, which is not a number anybody configured and not a number
// the operator should be told to aim for. The totals below it are rounded,
// because those are the points that would actually be awarded.
//
// The split total counts the WHOLE change, remainder included. Dropping the
// leftover lines flattered every piece size that did not divide evenly.
func fragmentPayoff(rules score.Rules) scoreSimFragment {
	const lines = 2000
	best, at := math.Inf(-1), 1
	for p := 1; p <= lines; p++ {
		if per := rules.SizeReward(float64(p)) / float64(p); per > best {
			best, at = per, p
		}
	}
	approved := func(n int) int {
		return score.Compute(rules, score.Input{Additions: n, Verdict: store.VerdictApproved, Attempt: 1}).Score
	}
	pieces := lines / at
	split := pieces * approved(at)
	if rest := lines - pieces*at; rest > 0 {
		split += approved(rest)
	}
	out := scoreSimFragment{Lines: at, Whole: approved(lines), Split: split}
	if out.Whole > 0 {
		out.Gain = float64(split) / float64(out.Whole)
	}
	return out
}
