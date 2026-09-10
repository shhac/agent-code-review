package store

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"
)

// prWhere is the identity predicate for a queue row. Six mutations select the
// row this way; naming it once means a change to how a PR is identified (repo
// casing, say) cannot land in five of them.
func prWhere(repo string, number int) string {
	return fmt.Sprintf("repo = %s AND number = %d", text(repo), number)
}

// Enqueue inserts or refreshes a queue row. On conflict:
//   - discovered_at keeps its first-seen value; a sweep re-seeing pending
//     work is not a new discovery, and bumping it would hide how long the PR
//     has actually been waiting.
//   - source only ever escalates to manual: a discovery sweep must not
//     downgrade a PR someone explicitly added (that would re-enable the
//     precheck they meant to bypass).
//   - holds merge PER NAME. A sweep rewrites the two names it owns and leaves
//     every other one alone, so it cannot disturb a hold set by somebody else
//     (the author's editing hold, say). A manual-source enqueue clears them
//     all, and no hold is re-imposed on a manual row: a manual add means
//     review this now.
//
// Storing holds (rather than deriving them at read time like the claim lease)
// is deliberate: a hold is a debounce frozen at the moment something asked for
// it, so editing the config dials never shrinks holds already granted, and a
// trigger with no derivable rule (an author opening the steering editor) can
// impose one at all.
func (d *duckDB) Enqueue(ctx context.Context, c Candidate) error {
	// An empty SHA would render as NULL: history.head_sha is NOT NULL, so
	// such a row could never Complete — it would error every cycle until
	// manually removed. Refuse it at the entrance instead.
	if c.HeadSHA == "" {
		return fmt.Errorf("enqueue %s#%d: empty head SHA", c.Repo, c.Number)
	}
	// json_merge_patch applies the incoming names over the stored ones and
	// leaves the rest untouched, which is the whole reason holds are a map:
	// the previous single-column form had to choose between the sweep's hold
	// and the row's, so a sweep could clear a hold it knew nothing about.
	//
	// The manual arm is name-scoped for exactly that reason. "Manual means
	// review this now" is a statement about the holds DISCOVERY imposed, not a
	// licence to clear the row: `source` never de-escalates, so a dashboard add
	// makes a row manual permanently, and nulling the column here meant every
	// later sweep wiped whatever else was holding the row — an author's editing
	// hold, on the commonest path there is. Promote still clears everything,
	// because a person pressing "review now" IS overriding all of it.
	//
	// No "only ever extends" comparison any more: hold names are rewritten by
	// their owner each sweep. Note this cannot RETIRE a name — an absent key
	// means "leave alone" in merge-patch semantics, and holds() omits a name
	// whose bound has passed — so a shortened config dial does not shorten a
	// hold already granted, exactly as the old form behaved.
	holdsMerge := fmt.Sprintf(`CASE
	    WHEN excluded.source = 'manual' OR queue.source = 'manual'
	      THEN json_merge_patch(COALESCE(queue.holds, '{}'), %s)
	    ELSE json_merge_patch(COALESCE(queue.holds, '{}'), excluded.holds) END`,
		clearHoldsJSON(DiscoveryHolds...))
	// Steering rides along only when the caller supplies one. Discovery
	// re-enqueues every sweep with no steering, so the conflict arms KEEP
	// whatever is there rather than writing NULL: a sweep must never wipe an
	// author's instruction. Supplying one is how a manual add sets steering
	// atomically, closing the window where a freed slot could claim the row
	// between the insert and a follow-up update.
	steerCase := func(column, incoming string) string {
		return fmt.Sprintf("CASE WHEN %s IS NOT NULL THEN %s ELSE queue.%s END", incoming, incoming, column)
	}
	msg, by, at := "NULL", "NULL", "NULL"
	if c.Steering != nil && c.Steering.Message != "" {
		msg, by, at = text(c.Steering.Message), nullText(c.Steering.SetBy), ts(c.Steering.SetAt)
	}
	sql := fmt.Sprintf(`INSERT INTO queue
	  (repo, number, type, title, author, url, head_sha, created_at, updated_at, queue_pos, discovered_at, source, holds, steering_message, steering_by, steering_at)
	VALUES (%s, %d, %s, %s, %s, %s, %s, %s, %s, %d, %s, %s, %s, %s, %s, %s)
	ON CONFLICT (repo, number) DO UPDATE SET
	  type = excluded.type,
	  title = excluded.title,
	  author = excluded.author,
	  url = excluded.url,
	  head_sha = excluded.head_sha,
	  updated_at = excluded.updated_at,
	  holds = `+holdsMerge+`,
	  steering_message = `+steerCase("steering_message", "excluded.steering_message")+`,
	  steering_by = `+steerCase("steering_by", "excluded.steering_by")+`,
	  steering_at = `+steerCase("steering_at", "excluded.steering_at")+`,
	  source = CASE WHEN excluded.source = 'manual' THEN 'manual' ELSE queue.source END`,
		nullText(c.Repo), c.Number, nullText(cmp.Or(c.Type, TypeNew)), nullText(c.Title), nullText(c.Author), nullText(c.URL), nullText(c.HeadSHA),
		ts(c.CreatedAt), ts(c.UpdatedAt), c.QueuePos, ts(c.DiscoveredAt), nullText(cmp.Or(c.Source, SourceDiscovered)),
		holdsJSON(c.Holds), msg, by, at)
	return d.exec(ctx, sql)
}

// QueuedPR returns one queued candidate. Callers that want a single row use
// this rather than filtering ListQueue: the predicate then lives in one place
// (prWhere, exact on repo, as every other single-row queue operation uses)
// instead of being split between an SQL filter and a Go comparison that
// disagreed about case folding.
func (d *duckDB) QueuedPR(ctx context.Context, repo string, number int) (Candidate, bool, error) {
	return queryOne(ctx, d, "SELECT * FROM queue WHERE "+prWhere(repo, number), scanCandidate)
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

// SetHolds adds or replaces named holds and leaves every other name alone.
// That per-name write is the whole point of the map: a caller can defer a PR
// without knowing what else is holding it, and without having to preserve
// something it never knew about.
//
// A SET rather than one name, because entries that must agree have to land
// together. A steering session's hold and the mark dating it were once two
// statements, and a failure between them left a mark with no hold — the state
// this file's own comments said must never exist.
//
// Names are always Hold*/Mark* constants, never caller input, which is what
// makes rendering them into the patch literal safe.
func (d *duckDB) SetHolds(ctx context.Context, repo string, number int, holds map[string]time.Time) error {
	if len(holds) == 0 {
		return nil
	}
	return d.exec(ctx, d.patchHolds(repo, number, holdsJSON(holds)))
}

// ClearHolds retires named holds. A JSON null patch removes exactly those
// keys, so a release can never expose a row that another hold still covers:
// the failure the single eligible_at column made unavoidable.
func (d *duckDB) ClearHolds(ctx context.Context, repo string, number int, names ...string) error {
	if len(names) == 0 {
		return nil
	}
	return d.exec(ctx, d.patchHolds(repo, number, clearHoldsJSON(names...)))
}

// patchHolds renders a per-name merge over the stored holds. Shared by both
// hold writers and shaped like Enqueue's conflict arm, so the merge rule has
// one definition rather than three that could drift apart.
func (d *duckDB) patchHolds(repo string, number int, patch string) string {
	return fmt.Sprintf("UPDATE queue SET holds = json_merge_patch(COALESCE(holds, '{}'), %s) WHERE %s",
		patch, prWhere(repo, number))
}

// Promote floats the row to the top (negative queue_pos sorts ahead of the
// default 0), clears EVERY hold, and escalates source to manual so the
// pre-review candidacy check is bypassed: one write, same semantics as
// removing and manually re-adding the PR at the front.
//
// Every hold, deliberately: "review now" is the escape hatch, and one that
// left some holds standing would be a button that sometimes does nothing.
func (d *duckDB) Promote(ctx context.Context, repo string, number int) error {
	return d.exec(ctx, fmt.Sprintf(
		"UPDATE queue SET queue_pos = -1, holds = NULL, source = 'manual' WHERE %s",
		prWhere(repo, number)))
}
