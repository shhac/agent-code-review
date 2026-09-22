package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// CostRates is one model's per-token prices, as EstimateCosts needs them.
// Deliberately a plain struct of floats rather than a pricing type: the store
// owns the columns and should not import the thing that fetches rates.
type CostRates struct {
	Input      float64
	Output     float64
	CacheWrite float64
	CacheRead  float64
}

// unpricedRows is the "could be valued but has not been" predicate: a class
// split recorded, no estimate yet. Named once because three queries select on
// it (list the models, apply the estimates, count what is left) and a drift
// between them would make the backfill report a number it did not do.
const unpricedRows = "est_cost_usd = 0 AND input_tokens + output_tokens > 0"

// UnpricedModels lists the models on rows that could be valued but have not
// been. That is a row completed while the price table was unreachable, or one
// written by a build that recorded the split before there was anywhere to put
// a valuation.
func (d *duckDB) UnpricedModels(ctx context.Context) ([]string, error) {
	rows, err := d.query(ctx, `SELECT DISTINCT model FROM history
	  WHERE `+unpricedRows+` AND model IS NOT NULL AND model <> ''`)
	if err != nil {
		return nil, err
	}
	models := make([]string, 0, len(rows))
	for _, r := range rows {
		if m := getString(r, "model"); m != "" {
			models = append(models, m)
		}
	}
	return models, nil
}

// EstimateCosts values every unpriced row whose model appears in rates. Done
// set-based, one UPDATE per model, rather than by reading rows and writing
// them back: history has no primary key, so there is no safe row identity to
// update against, and a model is exactly the grain the rates come at.
//
// Only ever fills a gap: the est_cost_usd = 0 guard means a re-run cannot
// revalue a row at newer rates, which keeps a recorded cost the cost at the
// time it was recorded.
func (d *duckDB) EstimateCosts(ctx context.Context, rates map[string]CostRates) (int64, error) {
	if len(rates) == 0 {
		return 0, nil
	}
	var b strings.Builder
	models := make([]string, 0, len(rates))
	for model := range rates {
		models = append(models, model)
	}
	sort.Strings(models) // deterministic statement order, so a log or a test can pin it
	for _, model := range models {
		r := rates[model]
		fmt.Fprintf(&b, `UPDATE history SET est_cost_usd =
		  input_tokens * %s + output_tokens * %s + cache_write_tokens * %s + cache_read_tokens * %s
		WHERE `+unpricedRows+` AND model = %s;`+"\n",
			num(r.Input), num(r.Output), num(r.CacheWrite), num(r.CacheRead), nullText(model))
	}
	before, err := d.countUnpriced(ctx)
	if err != nil {
		return 0, err
	}
	if err := d.exec(ctx, b.String()); err != nil {
		return 0, err
	}
	after, err := d.countUnpriced(ctx)
	if err != nil {
		return 0, err
	}
	return before - after, nil
}

func (d *duckDB) countUnpriced(ctx context.Context) (int64, error) {
	n, _, err := queryOne(ctx, d, "SELECT count(*) AS n FROM history WHERE "+unpricedRows, scanCount)
	return int64(n), err
}
