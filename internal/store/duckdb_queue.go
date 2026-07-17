package store

import (
	"context"
	"fmt"
	"strings"
)

// Enqueue inserts or refreshes a queue row. On conflict:
//   - discovered_at keeps its first-seen value; a sweep re-seeing pending
//     work is not a new discovery, and bumping it would hide how long the PR
//     has actually been waiting.
//   - source only ever escalates to manual: a discovery sweep must not
//     downgrade a PR someone explicitly added (that would re-enable the
//     precheck they meant to bypass).
//   - the eligibility hold only ever extends. A later eligible_at from this
//     sweep wins (the author is still active; push the hold out); an earlier
//     one loses (a hold, once set, does not shrink). A manual-source enqueue
//     clears the hold, and a hold is never re-imposed on a manual row.
func (d *duckDB) Enqueue(ctx context.Context, c Candidate) error {
	// An empty SHA would render as NULL: history.head_sha is NOT NULL, so
	// such a row could never Complete — it would error every cycle until
	// manually removed. Refuse it at the entrance instead.
	if c.HeadSHA == "" {
		return fmt.Errorf("enqueue %s#%d: empty head SHA", c.Repo, c.Number)
	}
	sql := fmt.Sprintf(`INSERT INTO queue
	  (repo, number, type, title, author, url, head_sha, created_at, updated_at, queue_pos, discovered_at, source, eligible_at, hold_reason)
	VALUES (%s, %d, %s, %s, %s, %s, %s, %s, %s, %d, %s, %s, %s, %s)
	ON CONFLICT (repo, number) DO UPDATE SET
	  type = excluded.type,
	  title = excluded.title,
	  author = excluded.author,
	  url = excluded.url,
	  head_sha = excluded.head_sha,
	  updated_at = excluded.updated_at,
	  eligible_at = CASE
	    WHEN excluded.source = 'manual' OR queue.source = 'manual' THEN NULL
	    WHEN COALESCE(excluded.eligible_at, TIMESTAMP '1970-01-01') > COALESCE(queue.eligible_at, TIMESTAMP '1970-01-01') THEN excluded.eligible_at
	    ELSE queue.eligible_at END,
	  hold_reason = CASE
	    WHEN excluded.source = 'manual' OR queue.source = 'manual' THEN NULL
	    WHEN COALESCE(excluded.eligible_at, TIMESTAMP '1970-01-01') > COALESCE(queue.eligible_at, TIMESTAMP '1970-01-01') THEN excluded.hold_reason
	    ELSE queue.hold_reason END,
	  source = CASE WHEN excluded.source = 'manual' THEN 'manual' ELSE queue.source END`,
		q(c.Repo), c.Number, q(orDefault(c.Type, TypeNew)), q(c.Title), q(c.Author), q(c.URL), q(c.HeadSHA),
		ts(c.CreatedAt), ts(c.UpdatedAt), c.QueuePos, ts(c.DiscoveredAt), q(orDefault(c.Source, SourceDiscovered)),
		tsp(c.EligibleAt), q(c.HoldReason))
	return d.exec(ctx, sql)
}

func (d *duckDB) ListQueue(ctx context.Context, repo string) ([]Candidate, error) {
	sql := "SELECT * FROM queue"
	if repo != "" {
		sql += " WHERE repo = " + q(repo)
	}
	// Manual queue positions win outright; among the default 0s the queue is
	// FIFO on first discovery: earlier-discovered work is actioned first, so
	// a fresh sweep can never leapfrog PRs already waiting. New-before-
	// Refreshed and PR number only break ties within one sweep instant.
	// NULLS FIRST: rows predating discovered_at tracking have waited longest.
	sql += " ORDER BY queue_pos, discovered_at ASC NULLS FIRST, CASE type WHEN 'new' THEN 0 ELSE 1 END, number"
	return queryMany(ctx, d, sql, scanCandidate)
}

// Claim is a compare-and-swap: the WHERE clause only matches an unclaimed
// row or a stale (abandoned) claim, and RETURNING tells us whether we won;
// one statement is one duckdb invocation, so the check and the write are
// atomic under DuckDB's file lock even across daemon instances.
func (d *duckDB) Claim(ctx context.Context, repo string, number int, l Lease) (bool, error) {
	rows, err := d.query(ctx, fmt.Sprintf(
		`UPDATE queue SET claimed_at = %s, work_dir = %s, claim_host = %s, claim_pid = %d
		 WHERE repo = %s AND number = %d AND (claimed_at IS NULL OR claimed_at < %s)
		 RETURNING 1 AS claimed`,
		ts(l.At), q(l.WorkDir), q(l.Host), l.PID,
		q(repo), number, ts(l.At.Add(-l.StaleAfter))))
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

func (d *duckDB) ClearClaim(ctx context.Context, repo string, number int) error {
	return d.exec(ctx, fmt.Sprintf(
		"UPDATE queue SET claimed_at = NULL, claim_host = NULL, claim_pid = NULL WHERE repo = %s AND number = %d",
		q(repo), number))
}

// Complete runs as one multi-statement batch; a single duckdb invocation is
// one connection, so BEGIN/COMMIT is a real transaction and a crash cannot
// leave the outcome recorded but the row still queued. The DELETE is gated on
// the reviewed head SHA: if new commits arrived mid-review (discovery updates
// head_sha on the claimed row), the row survives with its claim cleared so
// the next cycle reviews the newer commits.
func (d *duckDB) Complete(ctx context.Context, r Review) error {
	sql := fmt.Sprintf(`BEGIN;
	INSERT INTO history (repo, number, title, author, head_sha, verdict, engine, model, effort, codex_version, reviewed_at, duration_secs, work_dir, tokens_used) VALUES (%s, %d, %s, %s, %s, %s, %s, %s, %s, %s, %s, %d, %s, %d);
	DELETE FROM queue WHERE repo = %s AND number = %d AND head_sha IS NOT DISTINCT FROM %s;
	UPDATE queue SET claimed_at = NULL, claim_host = NULL, claim_pid = NULL WHERE repo = %s AND number = %d;
	COMMIT;`,
		q(r.Repo), r.Number, q(r.Title), q(r.Author), q(r.HeadSHA), q(r.Verdict), q(r.Engine), q(r.Model), q(r.Effort), q(r.CodexVersion), ts(r.ReviewedAt), r.DurationSecs, q(r.WorkDir), r.TokensUsed,
		q(r.Repo), r.Number, q(r.HeadSHA),
		q(r.Repo), r.Number)
	return d.exec(ctx, sql)
}

func (d *duckDB) Dequeue(ctx context.Context, repo string, number int) error {
	return d.exec(ctx, fmt.Sprintf("DELETE FROM queue WHERE repo = %s AND number = %d", q(repo), number))
}

func (d *duckDB) Reorder(ctx context.Context, positions []QueuePosition) error {
	if len(positions) == 0 {
		return nil
	}
	updates := make([]string, 0, len(positions))
	where := make([]string, 0, len(positions))
	for _, p := range positions {
		match := fmt.Sprintf("repo = %s AND number = %d", q(p.Repo), p.Number)
		updates = append(updates, fmt.Sprintf("WHEN %s THEN %d", match, p.Position))
		where = append(where, "("+match+")")
	}
	// A single UPDATE either applies every position or none, so a dashboard
	// reorder can never leave a partially reordered queue after an error.
	sql := "UPDATE queue SET queue_pos = CASE " + strings.Join(updates, " ") + " ELSE queue_pos END WHERE " + strings.Join(where, " OR ")
	return d.exec(ctx, sql)
}

// Promote floats the row to the top (negative queue_pos sorts ahead of the
// default 0), clears any eligibility hold, and escalates source to manual so
// the pre-review candidacy check is bypassed: one write, same semantics as
// removing and manually re-adding the PR at the front.
func (d *duckDB) Promote(ctx context.Context, repo string, number int) error {
	return d.exec(ctx, fmt.Sprintf(
		"UPDATE queue SET queue_pos = -1, eligible_at = NULL, hold_reason = NULL, source = 'manual' WHERE repo = %s AND number = %d",
		q(repo), number))
}
