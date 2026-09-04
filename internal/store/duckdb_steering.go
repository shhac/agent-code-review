package store

import (
	"context"
	"fmt"
	"time"
)

// SetSteering records the instruction shaping the next review of a PR,
// replacing any previous one. An empty message clears it, so the caller has
// one operation rather than two and cannot leave a half-set row behind.
//
// An UPDATE, not an upsert: steering belongs to a queued PR, so there is
// nothing to steer if the row is gone. handleSteering already 404s in that
// case; this makes it structurally true rather than a second check.
func (d *duckDB) SetSteering(ctx context.Context, repo string, number int, st Steering) error {
	if st.Message == "" {
		return d.ClearSteering(ctx, repo, number)
	}
	at := st.SetAt
	if at.IsZero() {
		at = time.Now()
	}
	return d.exec(ctx, fmt.Sprintf(
		`UPDATE queue SET steering_message = %s, steering_by = %s, steering_at = %s WHERE %s`,
		text(st.Message), nullText(st.SetBy), ts(at), prWhere(repo, number)))
}

func (d *duckDB) ClearSteering(ctx context.Context, repo string, number int) error {
	return d.exec(ctx, fmt.Sprintf(
		`UPDATE queue SET steering_message = NULL, steering_by = NULL, steering_at = NULL WHERE %s`,
		prWhere(repo, number)))
}
