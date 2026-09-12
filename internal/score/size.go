package score

// What a pull request's SHAPE is worth: one curve over how many lines it
// changed, and one straight line over how many it removed.
//
// This replaced a configurable ladder of tiers whose multipliers were
// interpolated between anchors. The ladder could draw any shape, which sounds
// like a virtue and was the problem: most of the shapes it could draw were
// incoherent, several of them silently, and the machinery that stopped the
// worst ones (derived tail anchors, a monotonicity rule, a step-or-ramp mode)
// existed only because the shape was drawn by hand. A closed form with named
// landmarks needs none of it, and every policy it can express is one somebody
// meant.

import "math"

// SizeReward is what a PR of this many changed lines earns before the verdict
// and the revision decay are applied.
//
//	          x^2
//	P * norm ------- ,  x = changed / (PieceLines * (falloff - 1))
//	        (1+x)^k
//
// The scale is chosen so that the two landmarks land exactly on the dials
// rather than near them:
//
//   - points per LINE peak at x = 1/(k-1), which is changed = PieceLines. The
//     size to aim for when splitting work up.
//   - points per PR peak at x = 2/(k-2), which is changed = Peak(). The best
//     a single pull request can do, and where SizePoints is paid.
//
// Quadratic near zero is the property that matters most. N fragments of a
// change earn about 1/N of shipping it whole, so atomising work LOSES without
// any floor dial to remember: the previous ruleset had points per line rising
// without limit as a PR shrank, and one-line pull requests were the optimal
// strategy.
func (r Rules) SizeReward(changed float64) float64 {
	if !(changed > 0) || !(r.SizeFalloff > 2) || !(r.PieceLines > 0) {
		return 0
	}
	x := changed / (r.PieceLines * (r.SizeFalloff - 1))
	return r.SizePoints * sizeNorm(r.SizeFalloff) * x * x / math.Pow(1+x, r.SizeFalloff)
}

// RemovalReward is what net removed lines earn, on top of the size reward and
// with no reference to it.
//
// Separate because it answers a different question. The size curve asks how
// much there was to review, and its whole point is that less is better for the
// same solve; a big deletion is not a sprawling solve, it is the outcome, and
// running it through a curve that punishes size is what made deleting 2000
// lines earn less than deleting 10.
//
// NET, so a pure move or rename earns nothing here: its value is in having
// been a manageable review, which the size curve already paid for. Gross
// deletions would pay a rewrite the same as a deletion, which is not the thing
// being rewarded.
//
// Linear, so it is split-neutral: a thousand lines removed pays the same in
// one PR or in twenty. The one seam that leaves is a balanced replacement
// split into a delete and an add, which collects a removal reward the combined
// PR would not. That is accepted rather than defended against, because the
// alternative is netting across related pull requests, and deleting first is
// usually the better way to do it anyway.
func (r Rules) RemovalReward(netRemoved float64) float64 {
	if netRemoved <= 0 {
		return 0
	}
	return r.RemovalPointsPer100 * netRemoved / 100
}

// Peak is the changed-line count that earns the most points as a single PR:
// the biggest a pull request should be before it starts costing its author.
func (r Rules) Peak() float64 {
	if !(r.SizeFalloff > 2) {
		return 0
	}
	return r.PieceLines * (r.SizeFalloff - 1) * 2 / (r.SizeFalloff - 2)
}

// sizeNorm makes the curve's maximum exactly SizePoints, whatever the falloff.
//
// Derived rather than a constant, so that moving the falloff dial changes the
// SHAPE and not the scale: with 27/4 hardcoded (which is this value at falloff
// 3) every other falloff quietly rescaled the whole leaderboard.
func sizeNorm(k float64) float64 {
	xp := 2 / (k - 2)
	return math.Pow(1+xp, k) / (xp * xp)
}

// The tier names, largest first in the order they are tested. They are LABELS:
// they explain a score rather than decide it, which is why their boundaries
// are multiples of the policy's own landmarks rather than another list of
// numbers to keep in step.
const (
	tierTiny   = "tiny"
	tierSmall  = "small"
	tierMedium = "medium"
	tierLarge  = "large"
	tierHuge   = "huge"
)

// Tier names where a PR sits against the policy.
//
//	tiny    below a quarter of the best piece size
//	small   up to the best piece size, so a well-judged piece of a stack
//	medium  up to the peak, so a well-judged pull request on its own
//	large   up to five times the peak
//	huge    past that
//
// At the shipped dials those fall at 12, 50, 200 and 1000 changed lines, which
// is close enough to the old hand-written ladder that a history row still
// reads the way it used to.
func (r Rules) Tier(changed float64) string {
	peak := r.Peak()
	switch {
	case changed <= r.PieceLines/4:
		return tierTiny
	case changed <= r.PieceLines:
		return tierSmall
	case peak <= 0 || changed <= peak:
		return tierMedium
	case changed <= peak*5:
		return tierLarge
	}
	return tierHuge
}

// Tiers are the label boundaries, for anything that has to explain the ladder
// rather than apply it. UpTo 0 is the open-ended last one.
func (r Rules) Tiers() []Tier {
	peak := r.Peak()
	return []Tier{
		{Name: tierTiny, UpTo: r.PieceLines / 4},
		{Name: tierSmall, UpTo: r.PieceLines},
		{Name: tierMedium, UpTo: peak},
		{Name: tierLarge, UpTo: peak * 5},
		{Name: tierHuge},
	}
}

// Tier is one label and the changed-line count it runs to.
type Tier struct {
	Name string  `json:"name"`
	UpTo float64 `json:"up_to,omitempty"`
}
