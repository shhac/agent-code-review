package score

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
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

// maxBase caps the points a single review can be worth before multipliers.
const maxBase = 1e6

// Validate reports why a ruleset cannot be used. config calls it after
// resolving; an invalid document falls back to DefaultRules rather than
// wedging every review, matching config.Read's tolerance of a corrupt file.
func (r Rules) Validate() error {
	// Bounded, not merely finite. An unbounded base multiplies out to a value
	// no history row can store, and the arithmetic stays finite the whole way
	// so nothing else catches it. The ceiling is far above any sane scale and
	// matches the bound `config set scoring.base` already enforces.
	if !(r.Base > 0) || r.Base > maxBase || math.IsNaN(r.Base) {
		return fmt.Errorf("base must be a positive number no greater than %v, got %v", float64(maxBase), r.Base)
	}
	if !(r.ChurnUnit > 0) || r.ChurnUnit > maxBase || math.IsNaN(r.ChurnUnit) {
		return fmt.Errorf("churn_unit must be a positive number no greater than %v, got %v", float64(maxBase), r.ChurnUnit)
	}
	if r.DeletionWeight < 0 || r.DeletionWeight > 1000 || math.IsNaN(r.DeletionWeight) {
		return fmt.Errorf("deletion_weight must be between 0 and 1000, got %v", r.DeletionWeight)
	}
	// Strictly below 1, not "at most 1". At exactly 1 nothing decays, and
	// comment-comment-approve (37.5+37.5+150=225) outscores a first-pass
	// approval (150): the one ordering this whole scheme exists to enforce,
	// inverted by a value the range would otherwise have called legal.
	if !(r.AttemptDecay > 0 && r.AttemptDecay < 1) {
		return fmt.Errorf("attempt_decay must be greater than 0 and less than 1, got %v (at 1 nothing decays, and repeated review rounds would outscore getting it right first time)", r.AttemptDecay)
	}
	for _, f := range []struct {
		name string
		val  float64
	}{
		{"shrink_bonus", r.ShrinkBonus},
		{"verdicts.approved", r.Approved},
		{"verdicts.commented", r.Commented},
		{"verdicts.requested_changes", r.RequestedChanges},
	} {
		if math.IsNaN(f.val) || math.IsInf(f.val, 0) {
			return fmt.Errorf("%s must be a finite number, got %v", f.name, f.val)
		}
	}
	// Empty is legal and means the default, like an unset multiplier. A
	// MISSPELLED one is not: it would score under the default while the config
	// file claims otherwise, and the point of naming a curve is to know which
	// one you are on.
	if r.Curve != "" && !ValidCurve(r.Curve) {
		return fmt.Errorf("curve is %q; valid: %s", r.Curve, strings.Join(Curves, ", "))
	}
	return validateBuckets(r.Buckets)
}

// validateBuckets enforces the shape bucketFor relies on: ascending bounds,
// and exactly one open-ended bucket, last. An open-ended bucket anywhere else
// matches everything and silently strands every bucket after it.
func validateBuckets(buckets []Bucket) error {
	if len(buckets) == 0 {
		return fmt.Errorf("at least one bucket is required")
	}
	prev := 0.0
	for i, b := range buckets {
		if b.Name == "" {
			return fmt.Errorf("bucket %d has no name", i)
		}
		if math.IsNaN(b.Multiplier) || math.IsInf(b.Multiplier, 0) {
			return fmt.Errorf("bucket %q: multiplier must be a finite number, got %v", b.Name, b.Multiplier)
		}
		last := i == len(buckets)-1
		if b.MaxChurn <= 0 {
			if !last {
				return fmt.Errorf("bucket %q is open-ended but is not last: it would match everything and strand the %d bucket(s) after it",
					b.Name, len(buckets)-i-1)
			}
			continue
		}
		if last {
			return fmt.Errorf("the last bucket (%q) must be open-ended (omit max_churn), or a PR larger than %v scores nothing",
				b.Name, b.MaxChurn)
		}
		if b.MaxChurn <= prev {
			return fmt.Errorf("bucket %q: max_churn %v must be greater than the previous bucket's %v", b.Name, b.MaxChurn, prev)
		}
		prev = b.MaxChurn
	}
	return nil
}
