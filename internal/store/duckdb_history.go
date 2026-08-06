package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// realVerdictsSQL is IsRealVerdict as a SQL predicate operand, built from the
// same realVerdicts list so the Go and SQL filters cannot drift.
var realVerdictsSQL = func() string {
	quoted := make([]string, len(realVerdicts))
	for i, v := range realVerdicts {
		quoted[i] = nullText(v)
	}
	return "(" + strings.Join(quoted, ", ") + ")"
}()

func (d *duckDB) LastReview(ctx context.Context, repo string, number int) (Review, bool, error) {
	return queryOne(ctx, d, fmt.Sprintf(
		"SELECT * FROM history WHERE repo = %s AND number = %d AND verdict IN %s ORDER BY reviewed_at DESC LIMIT 1",
		nullText(repo), number, realVerdictsSQL), scanReview)
}

func (d *duckDB) LastOutcome(ctx context.Context, repo string, number int) (Review, bool, error) {
	return queryOne(ctx, d, fmt.Sprintf(
		"SELECT * FROM history WHERE repo = %s AND number = %d ORDER BY reviewed_at DESC LIMIT 1", nullText(repo), number), scanReview)
}

func (d *duckDB) ListReviews(ctx context.Context, limit int) ([]Review, error) {
	if limit <= 0 {
		limit = 50
	}
	return queryMany(ctx, d, fmt.Sprintf(
		"SELECT * FROM history ORDER BY reviewed_at DESC LIMIT %d", limit), scanReview)
}

func (d *duckDB) ReviewByLogKey(ctx context.Context, repo string, number int, logKey string) (Review, bool, error) {
	rows, err := d.query(ctx, fmt.Sprintf(
		"SELECT * FROM history WHERE repo = %s AND number = %d ORDER BY reviewed_at DESC", nullText(repo), number))
	if err != nil {
		return Review{}, false, err
	}
	for _, row := range rows {
		r := scanReview(row)
		if r.LogKey == logKey {
			return r, true, nil
		}
	}
	return Review{}, false, nil
}

func (d *duckDB) ListReviewsSince(ctx context.Context, since time.Time) ([]Review, error) {
	// Zero means "no lower bound", matching FreshTokens below; without the
	// guard ts(zero) renders NULL and `>= NULL` silently matches nothing.
	sql := "SELECT * FROM history"
	if !since.IsZero() {
		sql += fmt.Sprintf(" WHERE reviewed_at >= %s", ts(since))
	}
	return queryMany(ctx, d, sql+" ORDER BY reviewed_at", scanReview)
}

func (d *duckDB) FreshTokens(ctx context.Context, since time.Time) (int64, error) {
	sql := "SELECT COALESCE(SUM(fresh_tokens), 0) AS total FROM history"
	if !since.IsZero() {
		sql += fmt.Sprintf(" WHERE reviewed_at >= %s", ts(since))
	}
	rows, err := d.query(ctx, sql)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return int64(getInt(rows[0], "total")), nil
}

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
	rows, err := d.query(ctx, `SELECT count(*) AS n FROM history
	  WHERE `+unpricedRows)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	return int64(getInt(rows[0], "n")), nil
}

// AppendHistory records an outcome WITHOUT touching the queue, which is the
// whole difference from Complete: an abandoned review still has work pending,
// so its row has to stay queued. The verdict is ERROR, which is deliberately
// not a "real" verdict, so the abandoned attempt cannot pass for a review that
// happened.
func (d *duckDB) AppendHistory(ctx context.Context, r Review) error {
	return d.exec(ctx, historyInsert(r))
}
