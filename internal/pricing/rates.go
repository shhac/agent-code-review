package pricing

// The pure pricing model: what a model's per-token prices are and what a run
// of a given shape costs at them. Split from cache.go's fetch/ETag/atomic-write
// machinery, which is a different job with a different failure mode — a reader
// asking "how is a review priced" should not have to read an HTTP poller to
// find out.

// Rates is one model's per-token prices, in USD. Zero means the database
// listed no price for that class, which for cache classes is common and
// normal: an engine with no explicit cache write has nothing to price.
type Rates struct {
	Input      float64 `json:"input_cost_per_token"`
	Output     float64 `json:"output_cost_per_token"`
	CacheWrite float64 `json:"cache_creation_input_token_cost"`
	// CacheWrite1h is the longer time-to-live cache write, billed above the
	// 5-minute one. claude reports the two tiers apart; we do not model which
	// tier a run used, so this is available for callers that read usage_raw.
	CacheWrite1h float64 `json:"cache_creation_input_token_cost_above_1hr"`
	CacheRead    float64 `json:"cache_read_input_token_cost"`
}

// Priced says whether the entry carries enough to value a run at all. A model
// present in the database but with no input or output price prices nothing.
func (r Rates) Priced() bool { return r.Input > 0 || r.Output > 0 }

// EffectiveCacheWrite is the rate a cache write is actually valued at: the
// database's cache-write price, falling back to the input rate when it prices
// no cache-write class, which is the closest honest answer since the model
// processed that content either way.
//
// Exported because the rule has to hold on BOTH paths that value a review. It
// used to live inside Cost, so the live path applied it and the store's
// backfill SQL (which is handed the raw rate fields) did not: the same review
// was worth two different amounts depending on which one priced it, and every
// backfilled cache-write-heavy row was silently under-valued.
func (r Rates) EffectiveCacheWrite() float64 {
	if r.CacheWrite == 0 {
		return r.Input
	}
	return r.CacheWrite
}

// Cost values one run's token classes.
func (r Rates) Cost(input, output, cacheWrite, cacheRead int) float64 {
	write := r.EffectiveCacheWrite()
	return float64(input)*r.Input +
		float64(output)*r.Output +
		float64(cacheWrite)*write +
		float64(cacheRead)*r.CacheRead
}
