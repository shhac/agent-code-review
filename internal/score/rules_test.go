package score

import (
	"math"
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
		"piece_lines":            func(r *Rules) { r.PieceLines = 80 },
		"size_points":            func(r *Rules) { r.SizePoints = 200 },
		"size_falloff":           func(r *Rules) { r.SizeFalloff = 2.5 },
		"removal_points_per_100": func(r *Rules) { r.RemovalPointsPer100 = 30 },
		"approved":               func(r *Rules) { r.Approved = 2 },
		"commented":              func(r *Rules) { r.Commented = 0.5 },
		"requested_changes":      func(r *Rules) { r.RequestedChanges = 0 },
		"attempt_decay":          func(r *Rules) { r.AttemptDecay = 0.5 },
		"exclude_paths":          func(r *Rules) { r.ExcludePaths = []string{"vendor/**"} },
		"use_gitattributes":      func(r *Rules) { r.UseGitattributes = false },
	}
	for name, tweak := range tweaks {
		r := DefaultRules()
		tweak(&r)
		if r.Hash() == a.Hash() {
			t.Errorf("changing %s did not change the hash", name)
		}
	}
}

func TestValidateRejectsBadRules(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Rules)
		want   string
	}{
		{"zero piece size", func(r *Rules) { r.PieceLines = 0 }, "piece_lines"},
		{"negative piece size", func(r *Rules) { r.PieceLines = -1 }, "piece_lines"},
		{"zero size points", func(r *Rules) { r.SizePoints = 0 }, "size_points"},
		{"negative removal reward", func(r *Rules) { r.RemovalPointsPer100 = -1 }, "removal_points_per_100"},
		{"falloff of exactly two", func(r *Rules) { r.SizeFalloff = 2 }, "size_falloff"},
		{"falloff below two", func(r *Rules) { r.SizeFalloff = 1.5 }, "size_falloff"},
		{"decay above one", func(r *Rules) { r.AttemptDecay = 1.5 }, "attempt_decay"},
		{"decay of zero", func(r *Rules) { r.AttemptDecay = 0 }, "attempt_decay"},
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

// A falloff of 2 or below has no peak: the curve rises forever, so a bigger
// pull request always earns more and the policy inverts. Proving the validator
// catches what the arithmetic would otherwise do quietly.
func TestFalloffAtTwoWouldInvertThePolicy(t *testing.T) {
	r := DefaultRules()
	r.SizeFalloff = 2
	if err := r.Validate(); err == nil {
		t.Fatal("size_falloff = 2 must be rejected")
	}

	// Demonstrating why, so the rule is not mistaken for arbitrary strictness.
	// SizeReward guards it too, so the shape is read directly here.
	rise := func(changed float64) float64 {
		x := changed / (r.PieceLines * (r.SizeFalloff - 1))
		return x * x / ((1 + x) * (1 + x))
	}
	if rise(10000) <= rise(1000) {
		t.Fatal("at falloff 2 a bigger PR should still be worth more; this test no longer shows the hazard")
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

// Dials large enough to overflow the history column must be rejected at
// validation, and clamped even so if a hand-edited config gets past it.
//
// Finite is not the same as storable: size_points 1e308 stays finite through
// the arithmetic, so no IsInf guard fired, and the int conversion saturated to
// 9223372036854775807 on its way into a 32-bit column.
func TestOversizedDialsAreRejectedAndClamped(t *testing.T) {
	r := DefaultRules()
	for _, points := range []float64{1e308, 1e12, maxPoints + 1} {
		r.SizePoints = points
		if err := r.Validate(); err == nil {
			t.Errorf("size_points %v must be rejected", points)
		}
	}

	// The backstop, for a config.json edited by hand.
	r.SizePoints = 1e308
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

func TestSubLinePiecesCannotSilentlyProduceAnUnscorableCurve(t *testing.T) {
	for _, piece := range []float64{0.5, 1e-200, math.SmallestNonzeroFloat64} {
		r := DefaultRules()
		r.PieceLines = piece
		if err := r.Validate(); err == nil {
			t.Errorf("piece_lines %v must be rejected, matching the CLI's one-line minimum", piece)
		}
	}
}
