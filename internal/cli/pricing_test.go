package cli

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/shhac/agent-code-review/internal/pricing"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
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

// fakePriceStore is the slice of store.Store backfillEstimates touches.
// Embeds the interface so any other call panics rather than silently
// returning a zero value.
type fakePriceStore struct {
	store.Store

	unpriced    []string
	unpricedErr error
	estimateErr error
	gotRates    map[string]store.CostRates
	valued      int64
	calls       int
}

func (f *fakePriceStore) UnpricedModels(context.Context) ([]string, error) {
	return f.unpriced, f.unpricedErr
}

func (f *fakePriceStore) EstimateCosts(_ context.Context, rates map[string]store.CostRates) (int64, error) {
	f.calls++
	f.gotRates = rates
	return f.valued, f.estimateErr
}

// seedPrices writes a price table into a temp dir and opens a cache over it,
// which is how a real daemon reads one: Open never touches the network.
func seedPrices(t *testing.T) *pricing.Cache {
	t.Helper()
	dir := t.TempDir()
	table := `{"listed-model":{"input_cost_per_token":2e-06,"output_cost_per_token":1e-05,` +
		`"cache_creation_input_token_cost":4e-06,"cache_read_input_token_cost":2e-07}}`
	if err := os.WriteFile(filepath.Join(dir, "model-prices.json"), []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	return pricing.Open(dir)
}

// TestBackfillEstimates covers the valuation orchestration, which was at 0%
// under both suites. It fails silently in the direction nobody would notice:
// spend simply reads as $0 on the dashboard forever, which looks like a cheap
// month rather than a broken meter.
func TestBackfillEstimates(t *testing.T) {
	quiet := func(string, ...any) {}

	t.Run("prices the models the table lists", func(t *testing.T) {
		fs := &fakePriceStore{unpriced: []string{"listed-model"}, valued: 3}
		backfillEstimates(context.Background(), seedPrices(t), fs, quiet)
		if fs.calls != 1 {
			t.Fatalf("EstimateCosts called %d times, want 1", fs.calls)
		}
		got, ok := fs.gotRates["listed-model"]
		if !ok {
			t.Fatalf("rates = %+v, want the listed model priced", fs.gotRates)
		}
		// The rates handed to the SQL must be the table's, not zeroes: a
		// zero-valued rate would price every review at exactly $0, which reads
		// as free rather than as unknown.
		if got.Input != 2e-06 || got.Output != 1e-05 || got.CacheWrite != 4e-06 || got.CacheRead != 2e-07 {
			t.Errorf("rates = %+v, want the table's figures", got)
		}
	})

	t.Run("an unlisted model stays unpriced rather than priced at zero", func(t *testing.T) {
		fs := &fakePriceStore{unpriced: []string{"model-nobody-lists"}}
		backfillEstimates(context.Background(), seedPrices(t), fs, quiet)
		if fs.calls != 0 {
			t.Errorf("nothing may be valued when no model resolves, got rates %+v", fs.gotRates)
		}
	})

	t.Run("a listed and an unlisted model together price only the listed one", func(t *testing.T) {
		fs := &fakePriceStore{unpriced: []string{"listed-model", "model-nobody-lists"}, valued: 1}
		backfillEstimates(context.Background(), seedPrices(t), fs, quiet)
		if len(fs.gotRates) != 1 {
			t.Errorf("rates = %+v, want only the model the table lists", fs.gotRates)
		}
	})

	t.Run("nothing unpriced does no work", func(t *testing.T) {
		fs := &fakePriceStore{}
		backfillEstimates(context.Background(), seedPrices(t), fs, quiet)
		if fs.calls != 0 {
			t.Error("an empty gap list must not reach the store")
		}
	})

	t.Run("a failed lookup is reported, not swallowed", func(t *testing.T) {
		var logged []string
		fs := &fakePriceStore{unpricedErr: errors.New("db gone")}
		backfillEstimates(context.Background(), seedPrices(t), fs, func(f string, a ...any) {
			logged = append(logged, fmt.Sprintf(f, a...))
		})
		if len(logged) == 0 {
			t.Error("a store failure here is invisible on the dashboard, so it must reach the log")
		}
	})

	t.Run("a failed estimate is reported", func(t *testing.T) {
		var logged []string
		fs := &fakePriceStore{unpriced: []string{"listed-model"}, estimateErr: errors.New("write failed")}
		backfillEstimates(context.Background(), seedPrices(t), fs, func(f string, a ...any) {
			logged = append(logged, fmt.Sprintf(f, a...))
		})
		if len(logged) == 0 {
			t.Error("a backfill that failed must say so")
		}
	})
}
