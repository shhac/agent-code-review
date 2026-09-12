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
// inventing a constant, and then pushed out far enough that the tail cannot
// pay a bigger PR less in total (see tailAnchor).
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
		return append(out, r.tailAnchor(out[0], tail.Multiplier, 2))
	default:
		last, prev := out[len(out)-1], out[len(out)-2]
		return append(out, r.tailAnchor(last, tail.Multiplier, last.Churn/prev.Churn))
	}
}

// maxTailStretch bounds how far the monotonicity rule may push the tail anchor
// beyond the ladder's own spacing. Past this the rule is not fixing an
// accidental dip, it is arguing with a policy that has deliberately chosen to
// pay a big PR less.
const maxTailStretch = 50

// tailAnchor places the open-ended tier's control point: ratio steps along the
// ladder, or further out if that would stop the last stretch paying a bigger
// PR less by ACCIDENT.
//
// Points are churn^exponent x rate, so a falling rate can outrun a rising
// churn. Over a segment from (x0,y0) to (x1,y1), interpolated on log churn,
// the total stops rising once the rate falls below (y0-y1)/(e*ln(x1/x0)) for
// exponent e, and the rate is lowest at x1. Monotonic therefore means
// ln(x1/x0) >= (y0-y1)/(e*y1).
//
// Whether that is worth enforcing depends on the policy. Under a proportional
// ruleset (exponent 1, which this used to be) a dip here is an accident: the
// ratio rule alone put the tail at 4000 and a 3700-churn PR outscored a
// 4000-churn one, so trimming 300 lines from an already-huge PR paid better
// than writing them, with no visible boundary to blame. Pushing the anchor to
// 4500 fixed it. Under the shipped exponent of 0.15 the decline is the POINT,
// the requirement comes out past 20 million churn, and enforcing it would be
// arguing with the operator; maxTailStretch is where the rule gives up.
//
// A tail multiplier of zero or less is likewise left alone: the rate ends at
// nothing whatever the distance, so there is no monotone tail to find.
// Validation permits it (a policy of "past here, nothing" is a choice an
// operator may make), and Compute stays total either way.
func (r Rules) tailAnchor(last Anchor, multiplier, ratio float64) Anchor {
	churn := last.Churn * ratio
	if multiplier > 0 && multiplier < last.Multiplier && r.ChurnExponent > 0 {
		needed := last.Churn * math.Exp((last.Multiplier-multiplier)/(r.ChurnExponent*multiplier))
		if needed > churn && needed <= last.Churn*maxTailStretch {
			churn = roundUpTidy(needed)
		}
	}
	return Anchor{Churn: churn, Multiplier: multiplier}
}

// roundUpTidy rounds up to two significant figures.
//
// Only the DERIVED tail goes through this: the monotonicity bound comes out
// as 4481.689070338063, which is a true number and a terrible thing to label
// an axis with. Rounding UP keeps the bound satisfied, and two figures is
// enough to stay in proportion with a ladder written in round numbers.
func roundUpTidy(x float64) float64 {
	if x <= 0 || math.IsInf(x, 0) || math.IsNaN(x) {
		return x
	}
	step := math.Pow(10, math.Floor(math.Log10(x))-1)
	return math.Ceil(x/step) * step
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
