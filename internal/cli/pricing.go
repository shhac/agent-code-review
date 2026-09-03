// Valuing a review in USD. Two paths compute the same figure and must not
// drift: estimator prices a review as it completes, costRates flattens the
// same rates for the backfill SQL. They live together so the invariant has one
// home, and TestCostRatesAgreesWithLivePricing can reach both.

package cli

import (
	"context"

	"github.com/shhac/agent-code-review/internal/pricing"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/scheduler"
	"github.com/shhac/agent-code-review/internal/store"
)

// costRates maps a model's prices onto the flat per-class figures the store's
// backfill SQL multiplies by. The EFFECTIVE cache-write rate, not the raw one:
// the SQL has no fallback of its own, so handing it the raw field valued a
// cache write at zero wherever the price table lists no cache-write class,
// while a live review priced the same tokens at the input rate. One named
// mapping so the two paths cannot drift again.
func costRates(r pricing.Rates) store.CostRates {
	return store.CostRates{
		Input:      r.Input,
		Output:     r.Output,
		CacheWrite: r.EffectiveCacheWrite(),
		CacheRead:  r.CacheRead,
	}
}

// backfillEstimates values rows that could be priced but were not: completed
// while the price table was unreachable, or written by a build that recorded
// the token split before there was anywhere to record a valuation. Runs after
// every price check rather than once at boot, so a daemon that started
// offline settles as soon as the table arrives.
//
// Gap-filling only. A row that already carries an estimate keeps it, so
// today's rates never rewrite what a past review cost.
func backfillEstimates(ctx context.Context, prices *pricing.Cache, s store.Store, logf scheduler.Logf) {
	models, err := s.UnpricedModels(ctx)
	if err != nil || len(models) == 0 {
		if err != nil {
			logf("pricing: could not look for unpriced reviews: %v", err)
		}
		return
	}
	rates := make(map[string]store.CostRates, len(models))
	for _, model := range models {
		r, ok := prices.Lookup(model)
		if !ok {
			continue // an unlisted model stays unpriced, rather than priced at zero
		}
		rates[model] = costRates(r)
	}
	if len(rates) == 0 {
		return
	}
	n, err := s.EstimateCosts(ctx, rates)
	if err != nil {
		logf("pricing: backfill failed: %v", err)
		return
	}
	if n > 0 {
		logf("pricing: valued %d review(s) that had no cost recorded", n)
	}
}

// estimator adapts the price table to the scheduler's PriceFn. A model the
// table does not list, or a review with no class split, yields false: the row
// records no estimate rather than a zero that would read as a free review.
func estimator(prices *pricing.Cache) scheduler.PriceFn {
	return func(model string, t review.TokenUsage) (float64, bool) {
		if t.Input+t.Output == 0 {
			return 0, false
		}
		rates, ok := prices.Lookup(model)
		if !ok {
			return 0, false
		}
		return rates.Cost(t.Input, t.Output, t.CacheWrite, t.CacheRead), true
	}
}
