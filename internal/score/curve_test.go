package score

import "testing"

// rateAt is what a PR of this churn is paid at, read the way Compute reads it:
// the tier resolved once, then asked what it is worth under the curve.
func rateAt(r Rules, churn float64) float64 {
	return sizeMultiplier(r, bucketFor(r.Buckets, churn), churn)
}

// The curve exists because a step ladder puts a cliff at every boundary: one
// line more than 1000 churn cost 60% of the rate. Interpolating removes the
// cliffs without widening what any decomposition can gain.
func TestLinearCurveRemovesTheCliffs(t *testing.T) {
	step := DefaultRules()
	step.Curve = CurveStep
	curve := DefaultRules()

	worst := func(r Rules) float64 {
		var w float64
		for n := 2; n < 4000; n++ {
			a := rateAt(r, float64(n))
			b := rateAt(r, float64(n+1))
			if a > 0 && (a-b)/a > w {
				w = (a - b) / a
			}
		}
		return w
	}
	if got := worst(step); got < 0.5 {
		t.Fatalf("step ladder's worst drop = %.1f%%; this test no longer shows the problem", got*100)
	}
	if got := worst(curve); got > 0.02 {
		t.Errorf("curve's worst single-line drop = %.1f%%, want under 2%%", got*100)
	}
}

// The bound on what splitting a change can earn is best rate over worst rate,
// and interpolation moves neither end. If this ever widens, the farming guard
// has been weakened by a change that looks purely cosmetic.
func TestLinearCurveDoesNotWidenTheFarmingBound(t *testing.T) {
	for _, r := range []Rules{func() Rules { c := DefaultRules(); c.Curve = CurveStep; return c }(), DefaultRules()} {
		lo, hi := 1e9, 0.0
		for n := 1; n < 6000; n++ {
			m := rateAt(r, float64(n))
			if m < lo {
				lo = m
			}
			if m > hi {
				hi = m
			}
		}
		if ratio := hi / lo; ratio > 7.6 {
			t.Errorf("curve %q: farm bound %.1fx, want the multiplier spread of 7.5x", r.Curve, ratio)
		}
	}
}

// Repeating a multiplier on two consecutive buckets holds it flat between
// them, which is what keeps the peak a range worth aiming at rather than an
// exact number worth hitting.
func TestRepeatedMultiplierKeepsAPlateau(t *testing.T) {
	r := DefaultRules()
	r.Curve = CurveLinear
	r.Buckets = []Bucket{
		{Name: "tiny", MaxChurn: 10, Multiplier: 1.0},
		{Name: "small-lo", MaxChurn: 20, Multiplier: 1.5},
		{Name: "small-hi", MaxChurn: 50, Multiplier: 1.5},
		{Name: "medium", MaxChurn: 250, Multiplier: 1.0},
		{Name: "huge", Multiplier: 0.2},
	}
	for _, churn := range []float64{20, 30, 40, 50} {
		if got := rateAt(r, churn); got != 1.5 {
			t.Errorf("churn %v: multiplier %v, want a flat 1.5 across the plateau", churn, got)
		}
	}
	if got := rateAt(r, 51); got >= 1.5 || got < 1.4 {
		t.Errorf("churn 51: multiplier %v, want just under the plateau, not a cliff", got)
	}
}

// The tier NAME still describes the size under either curve: it is what makes
// a score explainable, and only the number it is worth changes.
func TestCurveDoesNotChangeTheBucketName(t *testing.T) {
	r := DefaultRules()
	r.Curve = CurveLinear
	for _, tc := range []struct {
		churn int
		want  string
	}{{5, "tiny"}, {40, "small"}, {200, "medium"}, {900, "large"}, {5000, "huge"}} {
		got := Compute(r, Input{Additions: tc.churn, Verdict: verdictApproved, Attempt: 1})
		if got.Bucket != tc.want {
			t.Errorf("churn %d: bucket %q, want %q", tc.churn, got.Bucket, tc.want)
		}
	}
}

// Outside the anchors the rate holds flat, at the first tier's figure below
// the ladder and the tail's above it.
//
// Pinned by name rather than left to the range-scanning tests above, which
// assert aggregate bounds and would still pass if either end sloped away. The
// dashboard's port has this test; the authority it ports FROM should not be
// the half without it.
func TestCurveIsFlatOutsideTheAnchors(t *testing.T) {
	r := DefaultRules()
	for _, tc := range []struct {
		churn float64
		want  float64
	}{{1, 1.0}, {5, 1.0}, {10, 1.0}, {4000, 0.2}, {50000, 0.2}} {
		if got := rateAt(r, tc.churn); got != tc.want {
			t.Errorf("churn %v: multiplier %v, want a flat %v", tc.churn, got, tc.want)
		}
	}
}

// The tail anchor is DERIVED, and the dashboard draws the curve from these,
// so what it derives is a published fact rather than an implementation
// detail: 1000 x (1000/250) is why a "huge" PR is paid at 0.35x at 2000 churn
// instead of a flat 0.2x.
func TestAnchorsCarryTheLadderSpacingIntoTheTail(t *testing.T) {
	got := DefaultRules().Anchors()
	want := []Anchor{{10, 1.0}, {50, 1.5}, {250, 1.0}, {1000, 0.5}, {4000, 0.2}}
	if len(got) != len(want) {
		t.Fatalf("anchors = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("anchor %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// A ladder of one open-ended bucket is a flat multiplier, not a divide by zero.
func TestCurveHandlesADegenerateLadder(t *testing.T) {
	r := DefaultRules()
	r.Curve = CurveLinear
	r.Buckets = []Bucket{{Name: "flat", Multiplier: 0.8}}
	for _, churn := range []float64{1, 100, 10000} {
		if got := rateAt(r, churn); got != 0.8 {
			t.Errorf("churn %v: multiplier %v, want a flat 0.8", churn, got)
		}
	}
}

// Changing the curve must change the hash, or rows scored under the old shape
// would claim to be current.
func TestCurveIsHashed(t *testing.T) {
	step := DefaultRules()
	step.Curve = CurveStep
	curve := DefaultRules()
	if step.Hash() == curve.Hash() {
		t.Error("the curve must be part of the ruleset hash")
	}
	if err := curve.Validate(); err != nil {
		t.Errorf("the linear curve should validate: %v", err)
	}
	bad := DefaultRules()
	bad.Curve = "wobbly"
	if bad.Validate() == nil {
		t.Error("an unknown curve must be rejected")
	}
}
