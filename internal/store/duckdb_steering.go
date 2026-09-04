package store

import (
	"context"
	"fmt"
)

func (d *duckDB) SetSteering(ctx context.Context, st Steering) error {
	return d.exec(ctx, fmt.Sprintf(
		`INSERT INTO steering (repo, number, message, set_by, set_at)
		 VALUES (%s, %d, %s, %s, %s)
		 ON CONFLICT (repo, number) DO UPDATE SET
		   message = excluded.message, set_by = excluded.set_by, set_at = excluded.set_at`,
		text(st.Repo), st.Number, text(st.Message), text(st.SetBy), ts(st.SetAt)))
}

func (d *duckDB) ClearSteering(ctx context.Context, repo string, number int) error {
	return d.exec(ctx, "DELETE FROM steering WHERE "+prWhere(repo, number))
}

func (d *duckDB) Steering(ctx context.Context, repo string, number int) (Steering, bool, error) {
	return queryOne(ctx, d, "SELECT * FROM steering WHERE "+prWhere(repo, number), scanSteering)
}

// ListSteering returns every steering row, for the dashboard to render
// alongside the queue without a query per candidate.
func (d *duckDB) ListSteering(ctx context.Context) ([]Steering, error) {
	return queryMany(ctx, d, "SELECT * FROM steering ORDER BY set_at DESC", scanSteering)
}

func scanSteering(m map[string]any) (Steering, error) {
	r := &row{values: m}
	return Steering{
		Repo:    r.str("repo"),
		Number:  r.int("number"),
		Message: r.str("message"),
		SetBy:   r.str("set_by"),
		SetAt:   r.time("set_at"),
	}, r.err
}
