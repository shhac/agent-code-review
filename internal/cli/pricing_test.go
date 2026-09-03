package cli

import (
	"math"
	"testing"

	"github.com/shhac/agent-code-review/internal/pricing"
	"github.com/shhac/agent-code-review/internal/review"
)

// TestEstimatorRefusesToGuess pins the two ways estimator must decline. Both
// mean "cannot estimate", and both must report false rather than a zero the
// caller would record as a genuinely free review.
func TestEstimatorRefusesToGuess(t *testing.T) {
	// An empty cache lists no model, which is the unlisted-model case.
	est := estimator(pricing.Open(t.TempDir()))

	if _, ok := est("gpt-5.6", review.TokenUsage{Input: 1000, Output: 200}); ok {
		t.Error("a model the price table does not list must not be estimated")
	}
	if _, ok := est("gpt-5.6", review.TokenUsage{CacheWrite: 5000, CacheRead: 900000}); ok {
		t.Error("a review with no input/output split must not be estimated, even with cache tokens")
	}
}

func TestCostRatesAgreesWithLivePricing(t *testing.T) {
	const input, output, cacheWrite, cacheRead = 1000, 200, 50000, 900000

	for _, tc := range []struct {
		name  string
		rates pricing.Rates
	}{
		{"table prices no cache-write class", pricing.Rates{Input: 3e-6, Output: 15e-6, CacheRead: 3e-7}},
		{"table prices one", pricing.Rates{Input: 3e-6, Output: 15e-6, CacheWrite: 375e-8, CacheRead: 3e-7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live := tc.rates.Cost(input, output, cacheWrite, cacheRead)

			// What the backfill SQL computes, from the rates it is handed.
			cr := costRates(tc.rates)
			backfilled := float64(input)*cr.Input + float64(output)*cr.Output +
				float64(cacheWrite)*cr.CacheWrite + float64(cacheRead)*cr.CacheRead

			if math.Abs(live-backfilled) > 1e-12 {
				t.Errorf("live = %v, backfill = %v: the same review must be worth the same "+
					"figure whichever path prices it", live, backfilled)
			}
			if cr.CacheWrite == 0 && cacheWrite > 0 {
				t.Error("cache writes must never be valued at zero when the run performed them")
			}
		})
	}
}
