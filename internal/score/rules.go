package score

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
)

// Hash identifies the ruleset a score was computed under.
//
// It is what makes "tuning changes scores going forwards" a fact rather than a
// promise: every scored row records the hash of the rules that produced it, so
// "which rows predate the current policy" is a query, and a recompute can be
// aimed at exactly those rather than at all of history. Nobody's points move
// because somebody tried a different multiplier.
//
// Rules has no maps by construction (config resolves its optional, map-shaped
// document into this flat one), so encoding/json emits fields in declaration
// order and the digest is stable across runs without any canonicalisation
// step. Adding a map to Rules would quietly break that, which is the reason
// the resolved type is kept flat.
func (r Rules) Hash() string {
	b, err := json.Marshal(r)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// maxPoints caps what a single dial can be worth before the arithmetic stops
// fitting anywhere useful.
const maxPoints = 1e6

// Validate reports why a ruleset cannot be used. config calls it after
// resolving; an invalid document falls back to DefaultRules rather than
// wedging every review, matching config.Read's tolerance of a corrupt file.
func (r Rules) Validate() error {
	// A PR cannot contain a fraction of a changed line. Tiny positive scales
	// also overflow x before the reward can fall to zero. Match the CLI floor.
	if !(r.PieceLines >= 1) || r.PieceLines > maxPoints {
		return fmt.Errorf("piece_lines must be between 1 and %v, got %v", float64(maxPoints), r.PieceLines)
	}
	if !(r.SizePoints > 0) || r.SizePoints > maxPoints || math.IsNaN(r.SizePoints) {
		return fmt.Errorf("size_points must be a positive number no greater than %v, got %v", float64(maxPoints), r.SizePoints)
	}
	// Strictly above 2. At 2 and below the curve has no peak to fall from: it
	// rises forever, so a bigger pull request always earns more and the whole
	// policy inverts. The ceiling bounds how steeply the tail can decline.
	if !(r.SizeFalloff > 2) || r.SizeFalloff > 12 || math.IsNaN(r.SizeFalloff) {
		return fmt.Errorf("size_falloff must be greater than 2 and no greater than 12, got %v (at 2 or below a bigger PR always earns more)", r.SizeFalloff)
	}
	// Zero is legal and switches the removal reward off. Negative is not: it
	// would charge somebody for deleting code, which no configuration of this
	// tool should be able to say by accident.
	if r.RemovalPointsPer100 < 0 || r.RemovalPointsPer100 > maxPoints || math.IsNaN(r.RemovalPointsPer100) {
		return fmt.Errorf("removal_points_per_100 must be between 0 and %v, got %v", float64(maxPoints), r.RemovalPointsPer100)
	}
	// Strictly below 1, not "at most 1". At exactly 1 nothing decays, and
	// comment-comment-approve outscores a first-pass approval: the one
	// ordering this whole scheme exists to enforce, inverted by a value the
	// range would otherwise have called legal.
	if !(r.AttemptDecay > 0 && r.AttemptDecay < 1) {
		return fmt.Errorf("attempt_decay must be greater than 0 and less than 1, got %v (at 1 nothing decays, and repeated review rounds would outscore getting it right first time)", r.AttemptDecay)
	}
	for _, f := range []struct {
		name string
		val  float64
	}{
		{"verdicts.approved", r.Approved},
		{"verdicts.commented", r.Commented},
		{"verdicts.requested_changes", r.RequestedChanges},
	} {
		if math.IsNaN(f.val) || math.IsInf(f.val, 0) {
			return fmt.Errorf("%s must be a finite number, got %v", f.name, f.val)
		}
	}
	return nil
}
