package store

import (
	"cmp"
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
//
// Storing the hold (rather than deriving it at read time like the claim
// lease) is deliberate: the hold is a debounce frozen at discovery, so
// editing the hold dials never shrinks or lifts holds already granted, and
// future triggers (e.g. an explicit delay-on-click) can impose holds that no
// derivation could reconstruct.
// prWhere is the identity predicate for a queue row. Six mutations select the
// row this way; naming it once means a change to how a PR is identified (repo
// casing, say) cannot land in five of them.
func prWhere(repo string, number int) string {
	return fmt.Sprintf("repo = %s AND number = %d", text(repo), number)
}

func (d *duckDB) Enqueue(ctx context.Context, c Candidate) error {
	// An empty SHA would render as NULL: history.head_sha is NOT NULL, so
	// such a row could never Complete — it would error every cycle until
	// manually removed. Refuse it at the entrance instead.
	if c.HeadSHA == "" {
		return fmt.Errorf("enqueue %s#%d: empty head SHA", c.Repo, c.Number)
	}
	// The eligible_at and hold_reason CASE arms must stay in lockstep (the
	// reason always describes the timestamp it rides with), so both arms are
	// built from the same predicate strings rather than repeating them.
	const (
		manualWins = `excluded.source = 'manual' OR queue.source = 'manual'`
		newerHold  = `COALESCE(excluded.eligible_at, TIMESTAMP '1970-01-01') > COALESCE(queue.eligible_at, TIMESTAMP '1970-01-01')`
	)
	holdCase := func(column string) string {
		return fmt.Sprintf(`CASE
	    WHEN %s THEN NULL
	    WHEN %s THEN excluded.%s
	    ELSE queue.%s END`, manualWins, newerHold, column, column)
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
	  eligible_at = `+holdCase("eligible_at")+`,
	  hold_reason = `+holdCase("hold_reason")+`,
	  source = CASE WHEN excluded.source = 'manual' THEN 'manual' ELSE queue.source END`,
		nullText(c.Repo), c.Number, nullText(cmp.Or(c.Type, TypeNew)), nullText(c.Title), nullText(c.Author), nullText(c.URL), nullText(c.HeadSHA),
		ts(c.CreatedAt), ts(c.UpdatedAt), c.QueuePos, ts(c.DiscoveredAt), nullText(cmp.Or(c.Source, SourceDiscovered)),
		tsp(c.EligibleAt), nullText(c.HoldReason))
	return d.exec(ctx, sql)
}

func (d *duckDB) ListQueue(ctx context.Context, repo string) ([]Candidate, error) {
	sql := "SELECT * FROM queue"
	if repo != "" {
		sql += " WHERE repo = " + nullText(repo)
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
		 WHERE %s AND (claimed_at IS NULL OR claimed_at < %s)
		 RETURNING 1 AS claimed`,
		ts(l.At), nullText(l.WorkDir), nullText(l.Host), l.PID,
		prWhere(repo, number), ts(l.At.Add(-l.StaleAfter))))
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

func (d *duckDB) ClearClaim(ctx context.Context, repo string, number int) error {
	return d.exec(ctx, fmt.Sprintf(
		"UPDATE queue SET claimed_at = NULL, claim_host = NULL, claim_pid = NULL WHERE %s",
		prWhere(repo, number)))
}

// Complete runs as one multi-statement batch; a single duckdb invocation is
// one connection, so BEGIN/COMMIT is a real transaction and a crash cannot
// leave the outcome recorded but the row still queued. The DELETE is gated on
// the reviewed head SHA: if new commits arrived mid-review (discovery updates
// head_sha on the claimed row), the row survives with its claim cleared so
// the next cycle reviews the newer commits.
func (d *duckDB) Complete(ctx context.Context, r Review) error {
	// Steering needs no clause of its own: it lives on the row, so it is
	// retired by the same DELETE and survives the same stale-SHA path, where
	// the author's instruction still applies to the re-review of the newer
	// code. That used to be an EXISTS subquery re-deriving whether this very
	// DELETE was about to fire.
	pr := prWhere(r.Repo, r.Number)
	retiring := pr + " AND head_sha IS NOT DISTINCT FROM " + nullText(r.HeadSHA)
	sql := fmt.Sprintf(`BEGIN;
	%s
	DELETE FROM queue WHERE %s;
	UPDATE queue SET claimed_at = NULL, claim_host = NULL, claim_pid = NULL WHERE %s;
	COMMIT;`, historyInsert(r), retiring, pr)
	return d.exec(ctx, sql)
}

// historyInsert renders the one history INSERT both writers use: Complete,
// which retires the queue row with it, and AppendHistory, which does not.
// Shared so a new column cannot be added to one path and missed by the other.
func historyInsert(r Review) string {
	return fmt.Sprintf(`INSERT INTO history (repo, number, title, author, head_sha, verdict, engine, model, effort, engine_version, reviewed_at, duration_secs, work_dir, tokens_used, cost_usd, est_cost_usd, fresh_tokens, input_tokens, output_tokens, cache_write_tokens, cache_read_tokens, reasoning_tokens, usage_raw) VALUES (%s, %d, %s, %s, %s, %s, %s, %s, %s, %s, %s, %d, %s, %d, %s, %s, %d, %d, %d, %d, %d, %d, %s);`,
		nullText(r.Repo), r.Number, nullText(r.Title), nullText(r.Author), nullText(r.HeadSHA), nullText(r.Verdict), nullText(r.Engine), nullText(r.Model), nullText(r.Effort), nullText(r.EngineVersion), ts(r.ReviewedAt), r.DurationSecs, nullText(r.WorkDir), r.TokensUsed, num(r.CostUSD), num(r.EstCostUSD), r.FreshTokens, r.InputTokens, r.OutputTokens, r.CacheWriteTokens, r.CacheReadTokens, r.ReasoningTokens, nullText(r.UsageRaw))
}

func (d *duckDB) Dequeue(ctx context.Context, repo string, number int) error {
	return d.exec(ctx, fmt.Sprintf("DELETE FROM queue WHERE %s", prWhere(repo, number)))
}

func (d *duckDB) Reorder(ctx context.Context, positions []QueuePosition) error {
	if len(positions) == 0 {
		return nil
	}
	updates := make([]string, 0, len(positions))
	where := make([]string, 0, len(positions))
	for _, p := range positions {
		match := prWhere(p.Repo, p.Number)
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
		"UPDATE queue SET queue_pos = -1, eligible_at = NULL, hold_reason = NULL, source = 'manual' WHERE %s",
		prWhere(repo, number)))
}
