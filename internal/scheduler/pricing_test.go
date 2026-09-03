package scheduler

import (
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
)

// TestReviewRecordEstimate pins the rule the comments insist on and nothing
// asserted: a review we cannot value records NO estimate, never a zero that
// reads as a free review. The store round-trips EstCostUSD and the dashboard
// aggregates it, so both ends were tested and only this middle was not — a
// regression returning (0, true) would silently value every codex review at
// zero across every cost figure the dashboard shows.
func TestReviewRecordEstimate(t *testing.T) {
	c := store.Candidate{Repo: "o/r", Number: 1, HeadSHA: "s1"}
	v := review.Verdict{Decision: review.DecisionCommented, CostUSD: 1.25}
	prov := review.Provenance{Engine: "codex", Model: "some-model"}
	claimed := time.Now()

	t.Run("a priced model records our valuation", func(t *testing.T) {
		price := func(string, review.TokenUsage) (float64, bool) { return 0.42, true }
		got := reviewRecord(c, v, prov, claimed, price)
		if got.EstCostUSD != 0.42 {
			t.Errorf("EstCostUSD = %v, want 0.42", got.EstCostUSD)
		}
		// Recorded ALONGSIDE the engine's own figure, never replacing it: the
		// two side by side are the only check that our rates are right.
		if got.CostUSD != 1.25 {
			t.Errorf("CostUSD = %v, want the engine's own 1.25 untouched", got.CostUSD)
		}
	})

	t.Run("an unpriceable model records no estimate", func(t *testing.T) {
		price := func(string, review.TokenUsage) (float64, bool) { return 0, false }
		got := reviewRecord(c, v, prov, claimed, price)
		if got.EstCostUSD != 0 {
			t.Errorf("EstCostUSD = %v, want zero-value (absent)", got.EstCostUSD)
		}
		if got.CostUSD != 1.25 {
			t.Errorf("CostUSD = %v, want the engine's own figure untouched", got.CostUSD)
		}
	})

	t.Run("no price table at all is not a free review", func(t *testing.T) {
		got := reviewRecord(c, v, prov, claimed, nil)
		if got.EstCostUSD != 0 {
			t.Errorf("EstCostUSD = %v, want zero-value (absent)", got.EstCostUSD)
		}
	})
}
