package store

import (
	"context"
	"fmt"
	"time"
)

func (d *duckDB) ListRuns(ctx context.Context, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.query(ctx, fmt.Sprintf(
		"SELECT * FROM runs ORDER BY started_at DESC LIMIT %d", limit))
	if err != nil {
		return nil, err
	}
	return mapRows(rows, scanRun), nil
}

func (d *duckDB) RunningRuns(ctx context.Context) ([]Run, error) {
	rows, err := d.query(ctx, "SELECT * FROM runs WHERE status = 'running' ORDER BY started_at")
	if err != nil {
		return nil, err
	}
	return mapRows(rows, scanRun), nil
}

func (d *duckDB) ActiveRun(ctx context.Context, staleAfter time.Duration) (Run, bool, error) {
	cutoff := time.Now().Add(-staleAfter)
	return queryOne(ctx, d, fmt.Sprintf(
		"SELECT * FROM runs WHERE status = 'running' AND started_at >= %s ORDER BY started_at DESC LIMIT 1", ts(cutoff)), scanRun)
}

func (d *duckDB) StartRun(ctx context.Context, r Run) error {
	sql := fmt.Sprintf(
		"INSERT INTO runs (id, started_at, status, host, pid) VALUES (%s, %s, 'running', %s, %d)",
		q(r.ID), ts(r.StartedAt), q(r.Host), r.PID)
	_, err := d.query(ctx, sql)
	return err
}

func (d *duckDB) FinishRun(ctx context.Context, id string, status string) error {
	_, err := d.query(ctx, fmt.Sprintf(
		"UPDATE runs SET status = %s, finished_at = %s WHERE id = %s", q(status), ts(time.Now()), q(id)))
	return err
}
