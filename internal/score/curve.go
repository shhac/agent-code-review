package score

// How the size ladder is read: as a staircase, or as a curve through the
// tiers' figures.

import (
	"math"
	"slices"
)

// How a bucket's multiplier applies across its range.
const (
	// CurveStep holds one multiplier across a whole tier. Legible (the ladder
	// states in one line what any PR is worth) at the cost of a cliff at every
	// boundary: one line more than 1000 churn costs 60% of the rate.
	CurveStep = "step"
	// CurveLinear reads each bucket's max_churn as an ANCHOR and moves the
	// multiplier continuously between anchors, so no single line changes a
	// score much. Interpolated on log(churn), because tier boundaries are
	// spaced geometrically (10, 50, 250, 1000) and a linear walk between them
	// would spend almost all of its range near the low end.
	//
	// This is the SHIPPED DEFAULT, and the one an unset curve means. It does
	// not widen the farming bound: that is best rate over worst rate, and
	// interpolation moves neither end. What it removes is the cliff, which was
	// the only thing a step ladder taught people to game: the worst single
	// line cost drops from 60% of the rate to 0.4%. Repeating a multiplier on
	// two consecutive buckets keeps a PLATEAU, so the peak stays a range worth
	// aiming at rather than a knife-edge worth hitting exactly.
	CurveLinear = "linear"
)

// Curves are the valid values of scoring.curve.
var Curves = []string{CurveStep, CurveLinear}

// ValidCurve reports whether s names a curve.
func ValidCurve(s string) bool { return slices.Contains(Curves, s) }

// Anchor is one control point of the size curve: the churn at which a tier's
// multiplier applies exactly.
type Anchor struct {
	Churn      float64 `json:"churn"`
	Multiplier float64 `json:"multiplier"`
}

// Anchors turns the tier ladder into control points.
//
// Each bounded bucket anchors its multiplier at its own max_churn. The final
// bucket is open-ended and so has no churn of its own to sit at; its value is
// anchored one step further along the ladder's existing spacing (the ratio of
// the last two bounded anchors, or double the last one when there is only
// one), which keeps the tail's slope in proportion with the rest rather than
// inventing a constant.
//
// Exported because the dashboard DRAWS this curve, and that derived tail is
// policy rather than geometry: it is why the last anchor lands at 4000 and why
// a 2000-churn PR is paid at 0.35x. A chart that re-derived it in TypeScript
// would be a second opinion about what the tiers mean, and the two would
// eventually disagree in a way nobody would notice, because a chart is
// believed.
func (r Rules) Anchors() []Anchor {
	buckets := r.Buckets
	var out []Anchor
	for _, b := range buckets {
		if b.MaxChurn > 0 {
			out = append(out, Anchor{Churn: b.MaxChurn, Multiplier: b.Multiplier})
		}
	}
	if len(buckets) == 0 {
		return out
	}
	tail := buckets[len(buckets)-1]
	if tail.MaxChurn > 0 {
		return out // no open-ended bucket; validation forbids this, but do not guess
	}
	switch len(out) {
	case 0:
		// A single open-ended bucket: one flat multiplier everywhere.
		return []Anchor{{Churn: 1, Multiplier: tail.Multiplier}}
	case 1:
		return append(out, Anchor{Churn: out[0].Churn * 2, Multiplier: tail.Multiplier})
	default:
		ratio := out[len(out)-1].Churn / out[len(out)-2].Churn
		return append(out, Anchor{Churn: out[len(out)-1].Churn * ratio, Multiplier: tail.Multiplier})
	}
}

// sizeMultiplier is what the PR's size is worth, under the configured curve.
//
// The exception is named, not the default: an unset or unrecognised curve
// reads as the shipped one, the way an unrecognised scoring.mode reads as
// enabled. Selecting the other way round would make a Rules built without a
// curve score under the policy this repo argues AGAINST, silently.
//
// The tier is passed in rather than looked up again: Compute has already
// resolved it for the Result, and two lookups of the same ladder at the same
// churn is one more chance for them to disagree than is needed.
func sizeMultiplier(r Rules, tier Bucket, churn float64) float64 {
	if r.Curve == CurveStep {
		return tier.Multiplier
	}
	return interpolate(r.Anchors(), churn)
}

// interpolate reads the multiplier off the anchor curve, flat outside its ends.
func interpolate(anchors []Anchor, churn float64) float64 {
	if len(anchors) == 0 {
		return 0
	}
	if churn <= anchors[0].Churn {
		return anchors[0].Multiplier
	}
	for i := 1; i < len(anchors); i++ {
		x0, y0 := anchors[i-1].Churn, anchors[i-1].Multiplier
		x1, y1 := anchors[i].Churn, anchors[i].Multiplier
		if churn > x1 {
			continue
		}
		// Log scale: the ladder is geometric, so equal RATIOS of churn should
		// move the multiplier equally, not equal differences.
		t := (math.Log(churn) - math.Log(x0)) / (math.Log(x1) - math.Log(x0))
		return y0 + t*(y1-y0)
	}
	return anchors[len(anchors)-1].Multiplier
}
