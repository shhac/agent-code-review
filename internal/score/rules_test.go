package score

import (
	"strings"
	"testing"
)

func TestDefaultRulesAreValid(t *testing.T) {
	if err := DefaultRules().Validate(); err != nil {
		t.Fatalf("DefaultRules() is invalid: %v", err)
	}
}

func TestHashIsStableAndSensitive(t *testing.T) {
	a := DefaultRules()
	if a.Hash() != DefaultRules().Hash() {
		t.Error("the same rules must hash the same way on every call")
	}
	if len(a.Hash()) != 16 {
		t.Errorf("hash length = %d, want 16", len(a.Hash()))
	}

	// Every knob must move the hash, or a tuned score would look current.
	tweaks := map[string]func(*Rules){
		"base":              func(r *Rules) { r.Base = 200 },
		"deletion_weight":   func(r *Rules) { r.DeletionWeight = 0.25 },
		"approved":          func(r *Rules) { r.Approved = 2 },
		"commented":         func(r *Rules) { r.Commented = 0.5 },
		"requested_changes": func(r *Rules) { r.RequestedChanges = 0 },
		"shrink_bonus":      func(r *Rules) { r.ShrinkBonus = 1.5 },
		"attempt_decay":     func(r *Rules) { r.AttemptDecay = 0.5 },
		"bucket multiplier": func(r *Rules) { r.Buckets[1].Multiplier = 9 },
		"bucket bound":      func(r *Rules) { r.Buckets[1].MaxChurn = 60 },
		"bucket name":       func(r *Rules) { r.Buckets[1].Name = "smallish" },
	}
	for name, tweak := range tweaks {
		r := DefaultRules()
		tweak(&r)
		if r.Hash() == a.Hash() {
			t.Errorf("changing %s did not change the hash", name)
		}
	}
}

// Bucket ORDER is meaningful, so reordering must change the hash.
func TestHashIsOrderSensitiveForBuckets(t *testing.T) {
	a := DefaultRules()
	b := DefaultRules()
	b.Buckets[0], b.Buckets[1] = b.Buckets[1], b.Buckets[0]
	if a.Hash() == b.Hash() {
		t.Error("reordering buckets must change the hash")
	}
}

func TestValidateRejectsBadRules(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Rules)
		want   string
	}{
		{"zero base", func(r *Rules) { r.Base = 0 }, "base"},
		{"negative base", func(r *Rules) { r.Base = -1 }, "base"},
		{"negative deletion weight", func(r *Rules) { r.DeletionWeight = -1 }, "deletion_weight"},
		{"decay above one", func(r *Rules) { r.AttemptDecay = 1.5 }, "attempt_decay"},
		{"decay of zero", func(r *Rules) { r.AttemptDecay = 0 }, "attempt_decay"},
		{"no buckets", func(r *Rules) { r.Buckets = nil }, "at least one bucket"},
		{"unnamed bucket", func(r *Rules) { r.Buckets[0].Name = "" }, "no name"},
		{"descending bounds", func(r *Rules) { r.Buckets[1].MaxChurn = 5 }, "must be greater than"},
		{"equal bounds", func(r *Rules) { r.Buckets[1].MaxChurn = 10 }, "must be greater than"},
		{"bounded last bucket", func(r *Rules) { r.Buckets[len(r.Buckets)-1].MaxChurn = 5000 }, "must be open-ended"},
		{"open-ended in the middle", func(r *Rules) { r.Buckets[2].MaxChurn = 0 }, "is not last"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := DefaultRules()
			tc.mutate(&r)
			err := r.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Validate() = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// An open-ended bucket in the middle strands everything after it. Proving the
// validator catches what bucketFor would otherwise do silently.
func TestOpenEndedMiddleBucketWouldStrandTheRest(t *testing.T) {
	r := DefaultRules()
	r.Buckets[2].MaxChurn = 0
	if got := bucketFor(r.Buckets, 99999); got.Name != r.Buckets[2].Name {
		t.Fatalf("bucketFor picked %q; this test no longer demonstrates the hazard", got.Name)
	}
	if r.Validate() == nil {
		t.Error("Validate must reject what bucketFor would do silently")
	}
}

// attempt_decay of exactly 1 inverts the one ordering this scheme exists to
// enforce, so the range has to exclude it rather than merely discourage it.
func TestDecayOfOneIsRejectedBecauseItInvertsTheOrdering(t *testing.T) {
	r := DefaultRules()
	r.AttemptDecay = 1
	if err := r.Validate(); err == nil {
		t.Fatal("attempt_decay = 1 must be rejected")
	}

	// Demonstrating why, so the rule is not mistaken for arbitrary strictness.
	first := Compute(r, Input{Additions: 40, Deletions: 10, Verdict: verdictApproved, Attempt: 1}).Score
	slow := 0
	for i, v := range []string{verdictCommented, verdictCommented, verdictApproved} {
		slow += Compute(r, Input{Additions: 40, Deletions: 10, Verdict: v, Attempt: i + 1}).Score
	}
	if slow <= first {
		t.Fatalf("with no decay, comment rounds scored %d vs %d: this test no longer shows the hazard", slow, first)
	}
}

// A base large enough to overflow the history column must be rejected at
// validation, and clamped even so if a hand-edited config gets past it.
//
// Finite is not the same as storable: base 1e308 stays finite through every
// multiplier, so no IsInf guard fired, and the int conversion saturated to
// 9223372036854775807 on its way into a 32-bit column.
func TestOversizedBaseIsRejectedAndClamped(t *testing.T) {
	r := DefaultRules()
	for _, base := range []float64{1e308, 1e12, maxBase + 1} {
		r.Base = base
		if err := r.Validate(); err == nil {
			t.Errorf("base %v must be rejected", base)
		}
	}

	// The backstop, for a config.json edited by hand.
	r.Base = 1e308
	got := Compute(r, Input{Additions: 40, Deletions: 10, Verdict: verdictApproved, Attempt: 1})
	if got.Score != maxScore {
		t.Errorf("score = %d, want it clamped to %d", got.Score, maxScore)
	}
	r.RequestedChanges = -1
	got = Compute(r, Input{Additions: 40, Deletions: 10, Verdict: verdictRequestedChanges, Attempt: 1})
	if got.Score != -maxScore {
		t.Errorf("score = %d, want it clamped to %d", got.Score, -maxScore)
	}
}
