package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// realVerdictsSQL is IsRealVerdict as a SQL predicate operand, built from the
// same realVerdicts list so the Go and SQL filters cannot drift.
var realVerdictsSQL = func() string {
	quoted := make([]string, len(realVerdicts))
	for i, v := range realVerdicts {
		quoted[i] = q(v)
	}
	return "(" + strings.Join(quoted, ", ") + ")"
}()

func (d *duckDB) LastReview(ctx context.Context, repo string, number int) (Review, bool, error) {
	return queryOne(ctx, d, fmt.Sprintf(
		"SELECT * FROM history WHERE repo = %s AND number = %d AND verdict IN %s ORDER BY reviewed_at DESC LIMIT 1",
		q(repo), number, realVerdictsSQL), scanReview)
}

func (d *duckDB) LastOutcome(ctx context.Context, repo string, number int) (Review, bool, error) {
	return queryOne(ctx, d, fmt.Sprintf(
		"SELECT * FROM history WHERE repo = %s AND number = %d ORDER BY reviewed_at DESC LIMIT 1", q(repo), number), scanReview)
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
		"SELECT * FROM history WHERE repo = %s AND number = %d ORDER BY reviewed_at DESC", q(repo), number))
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
	return queryMany(ctx, d, fmt.Sprintf(
		"SELECT * FROM history WHERE reviewed_at >= %s ORDER BY reviewed_at", ts(since)), scanReview)
}

func (d *duckDB) TokensUsed(ctx context.Context, since time.Time) (int64, error) {
	sql := "SELECT COALESCE(SUM(tokens_used), 0) AS total FROM history"
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
