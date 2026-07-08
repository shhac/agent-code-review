package store

import (
	"context"
	_ "embed"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed schema.sql
var schemaSQL string

// duckDB is a subprocess-backed Store: every statement spawns a `duckdb` CLI
// process against the same file, exactly like agent-sql's driver. A mutex
// serializes access because DuckDB is single-writer per file and the daemon
// reviews up to N PRs concurrently.
type duckDB struct {
	bin  string
	path string
	mu   sync.Mutex
}

func newDuckDB(path string) (*duckDB, error) {
	if path == "" {
		return nil, stderrors.New("store path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	bin := "duckdb"
	if custom := os.Getenv("AGENT_CODE_REVIEW_DUCKDB_PATH"); custom != "" {
		bin = custom
	}
	if _, err := exec.LookPath(bin); err != nil {
		return nil, fmt.Errorf("DuckDB CLI not found (%s). Install with: brew install duckdb", bin)
	}
	return &duckDB{bin: bin, path: path}, nil
}

func (d *duckDB) Init(ctx context.Context) error {
	_, err := d.query(ctx, schemaSQL)
	return err
}

func (d *duckDB) Close() error { return nil }

// --- exec plumbing (mirrors agent-sql/internal/driver/duckdb) ---

func (d *duckDB) query(ctx context.Context, sql string) ([]map[string]any, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	args := []string{"-cmd", ".mode jsonlines", d.path, "-c", sql}
	cmd := exec.CommandContext(ctx, d.bin, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = "DuckDB query failed"
		}
		return nil, stderrors.New(msg)
	}
	return parseNDJSON(string(out))
}

// mapRows scans every result row through one scanner — the shared tail of all
// List* methods, so none can forget the preallocation or empty-slice contract.
func mapRows[T any](rows []map[string]any, scan func(map[string]any) T) []T {
	out := make([]T, 0, len(rows))
	for _, r := range rows {
		out = append(out, scan(r))
	}
	return out
}

func parseNDJSON(stdout string) ([]map[string]any, error) {
	var rows []map[string]any
	for _, line := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == "{" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(trimmed), &row); err != nil {
			return nil, fmt.Errorf("parse DuckDB output: %w", err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// --- queue ---

// Enqueue inserts or refreshes a queue row. On conflict:
//   - discovered_at keeps its first-seen value — a sweep re-seeing pending
//     work is not a new discovery, and bumping it would hide how long the PR
//     has actually been waiting.
//   - source only ever escalates to manual: a discovery sweep must not
//     downgrade a PR someone explicitly added (that would re-enable the
//     precheck they meant to bypass).
//   - the eligibility hold only ever extends. A later eligible_at from this
//     sweep wins (the author is still active — push the hold out); an earlier
//     one loses (a hold, once set, does not shrink). A manual-source enqueue
//     clears the hold, and a hold is never re-imposed on a manual row.
func (d *duckDB) Enqueue(ctx context.Context, c Candidate) error {
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
	_, err := d.query(ctx, sql)
	return err
}

func (d *duckDB) ListQueue(ctx context.Context, repo string) ([]Candidate, error) {
	sql := "SELECT * FROM queue"
	if repo != "" {
		sql += " WHERE repo = " + q(repo)
	}
	// Manual queue positions win outright; among the default 0s the queue is
	// FIFO on first discovery — earlier-discovered work is actioned first, so
	// a fresh sweep can never leapfrog PRs already waiting. New-before-
	// Refreshed and PR number only break ties within one sweep instant.
	// NULLS FIRST: rows predating discovered_at tracking have waited longest.
	sql += " ORDER BY queue_pos, discovered_at ASC NULLS FIRST, CASE type WHEN 'new' THEN 0 ELSE 1 END, number"
	rows, err := d.query(ctx, sql)
	if err != nil {
		return nil, err
	}
	return mapRows(rows, scanCandidate), nil
}

// Claim is a compare-and-swap: the WHERE clause only matches an unclaimed
// row or a stale (abandoned) claim, and RETURNING tells us whether we won —
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
	_, err := d.query(ctx, fmt.Sprintf(
		"UPDATE queue SET claimed_at = NULL, claim_host = NULL, claim_pid = NULL WHERE repo = %s AND number = %d",
		q(repo), number))
	return err
}

// Complete runs as one multi-statement batch — a single duckdb invocation is
// one connection, so BEGIN/COMMIT is a real transaction and a crash cannot
// leave the outcome recorded but the row still queued. The DELETE is gated on
// the reviewed head SHA: if new commits arrived mid-review (discovery updates
// head_sha on the claimed row), the row survives with its claim cleared so
// the next cycle reviews the newer commits.
func (d *duckDB) Complete(ctx context.Context, r Review) error {
	sql := fmt.Sprintf(`BEGIN;
	INSERT INTO history (repo, number, title, author, head_sha, verdict, engine, reviewed_at, duration_secs, work_dir, tokens_used) VALUES (%s, %d, %s, %s, %s, %s, %s, %s, %d, %s, %d);
	DELETE FROM queue WHERE repo = %s AND number = %d AND head_sha = %s;
	UPDATE queue SET claimed_at = NULL, claim_host = NULL, claim_pid = NULL WHERE repo = %s AND number = %d;
	COMMIT;`,
		q(r.Repo), r.Number, q(r.Title), q(r.Author), q(r.HeadSHA), q(r.Verdict), q(r.Engine), ts(r.ReviewedAt), r.DurationSecs, q(r.WorkDir), r.TokensUsed,
		q(r.Repo), r.Number, q(r.HeadSHA),
		q(r.Repo), r.Number)
	_, err := d.query(ctx, sql)
	return err
}

func (d *duckDB) Dequeue(ctx context.Context, repo string, number int) error {
	_, err := d.query(ctx, fmt.Sprintf("DELETE FROM queue WHERE repo = %s AND number = %d", q(repo), number))
	return err
}

func (d *duckDB) SetQueuePos(ctx context.Context, repo string, number int, pos int) error {
	_, err := d.query(ctx, fmt.Sprintf("UPDATE queue SET queue_pos = %d WHERE repo = %s AND number = %d", pos, q(repo), number))
	return err
}

// Promote floats the row to the top (negative queue_pos sorts ahead of the
// default 0), clears any eligibility hold, and escalates source to manual so
// the pre-review candidacy check is bypassed — one write, same semantics as
// removing and manually re-adding the PR at the front.
func (d *duckDB) Promote(ctx context.Context, repo string, number int) error {
	_, err := d.query(ctx, fmt.Sprintf(
		"UPDATE queue SET queue_pos = -1, eligible_at = NULL, hold_reason = NULL, source = 'manual' WHERE repo = %s AND number = %d",
		q(repo), number))
	return err
}

// --- history ---

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
	rows, err := d.query(ctx, fmt.Sprintf(
		"SELECT * FROM history WHERE repo = %s AND number = %d AND verdict IN %s ORDER BY reviewed_at DESC LIMIT 1",
		q(repo), number, realVerdictsSQL))
	if err != nil || len(rows) == 0 {
		return Review{}, false, err
	}
	return scanReview(rows[0]), true, nil
}

func (d *duckDB) LastOutcome(ctx context.Context, repo string, number int) (Review, bool, error) {
	rows, err := d.query(ctx, fmt.Sprintf(
		"SELECT * FROM history WHERE repo = %s AND number = %d ORDER BY reviewed_at DESC LIMIT 1", q(repo), number))
	if err != nil || len(rows) == 0 {
		return Review{}, false, err
	}
	return scanReview(rows[0]), true, nil
}

func (d *duckDB) ListReviews(ctx context.Context, limit int) ([]Review, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.query(ctx, fmt.Sprintf(
		"SELECT * FROM history ORDER BY reviewed_at DESC LIMIT %d", limit))
	if err != nil {
		return nil, err
	}
	return mapRows(rows, scanReview), nil
}

func (d *duckDB) ListReviewsSince(ctx context.Context, since time.Time) ([]Review, error) {
	rows, err := d.query(ctx, fmt.Sprintf(
		"SELECT * FROM history WHERE reviewed_at >= %s ORDER BY reviewed_at", ts(since)))
	if err != nil {
		return nil, err
	}
	return mapRows(rows, scanReview), nil
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

// --- allowed authors ---

func (d *duckDB) AllowAuthor(ctx context.Context, a AllowedAuthor) error {
	sql := fmt.Sprintf(`INSERT INTO allowed_authors (repo, github_handle, name, email, slack_id)
	VALUES (%s, %s, %s, %s, %s)
	ON CONFLICT (repo, github_handle) DO UPDATE SET
	  name = excluded.name, email = excluded.email, slack_id = excluded.slack_id`,
		q(a.Repo), q(a.GitHubHandle), q(a.Name), q(a.Email), q(a.SlackID))
	_, err := d.query(ctx, sql)
	return err
}

func (d *duckDB) DenyAuthor(ctx context.Context, repo, handle string) error {
	_, err := d.query(ctx, fmt.Sprintf(
		"DELETE FROM allowed_authors WHERE repo = %s AND lower(github_handle) = lower(%s)", q(repo), q(handle)))
	return err
}

func (d *duckDB) ListAllowedAuthors(ctx context.Context, repo string) ([]AllowedAuthor, error) {
	sql := "SELECT * FROM allowed_authors"
	if repo != "" {
		sql += " WHERE repo = " + q(repo)
	}
	// Alphabetical by author (the entity this list is about), case-insensitive
	// — DuckDB's default TEXT ordering would sort "Zed" before "alice". Repo
	// breaks ties for handles allowed in several places.
	sql += " ORDER BY lower(github_handle), lower(repo)"
	rows, err := d.query(ctx, sql)
	if err != nil {
		return nil, err
	}
	return mapRows(rows, scanAuthor), nil
}

func (d *duckDB) IsAuthorAllowed(ctx context.Context, repo, handle string) (bool, error) {
	if handle == "" {
		return false, nil
	}
	rows, err := d.query(ctx, fmt.Sprintf(
		"SELECT 1 FROM allowed_authors WHERE (repo = %s OR repo = %s) AND lower(github_handle) = lower(%s) LIMIT 1",
		q(repo), q(WildcardRepo), q(handle)))
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

// --- run-lock ---

func (d *duckDB) RunningRuns(ctx context.Context) ([]Run, error) {
	rows, err := d.query(ctx, "SELECT * FROM runs WHERE status = 'running' ORDER BY started_at")
	if err != nil {
		return nil, err
	}
	return mapRows(rows, scanRun), nil
}

func (d *duckDB) ActiveRun(ctx context.Context, staleAfter time.Duration) (Run, bool, error) {
	cutoff := time.Now().Add(-staleAfter)
	rows, err := d.query(ctx, fmt.Sprintf(
		"SELECT * FROM runs WHERE status = 'running' AND started_at >= %s ORDER BY started_at DESC LIMIT 1", ts(cutoff)))
	if err != nil || len(rows) == 0 {
		return Run{}, false, err
	}
	return scanRun(rows[0]), true, nil
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

// --- scan/format helpers ---

func scanReview(r map[string]any) Review {
	return Review{
		Repo:         getString(r, "repo"),
		Number:       getInt(r, "number"),
		Title:        getString(r, "title"),
		Author:       getString(r, "author"),
		HeadSHA:      getString(r, "head_sha"),
		Verdict:      getString(r, "verdict"),
		Engine:       getString(r, "engine"),
		ReviewedAt:   getTime(r, "reviewed_at"),
		DurationSecs: getInt(r, "duration_secs"),
		WorkDir:      getString(r, "work_dir"),
		TokensUsed:   getInt(r, "tokens_used"),
	}
}

func scanAuthor(r map[string]any) AllowedAuthor {
	return AllowedAuthor{
		Repo:         getString(r, "repo"),
		GitHubHandle: getString(r, "github_handle"),
		Name:         getString(r, "name"),
		Email:        getString(r, "email"),
		SlackID:      getString(r, "slack_id"),
	}
}

func scanRun(r map[string]any) Run {
	run := Run{
		ID:        getString(r, "id"),
		StartedAt: getTime(r, "started_at"),
		Status:    getString(r, "status"),
		Host:      getString(r, "host"),
		PID:       getInt(r, "pid"),
	}
	if t := getTime(r, "finished_at"); !t.IsZero() {
		run.FinishedAt = &t
	}
	return run
}

func scanCandidate(r map[string]any) Candidate {
	c := Candidate{
		Repo:         getString(r, "repo"),
		Number:       getInt(r, "number"),
		Type:         getString(r, "type"),
		Title:        getString(r, "title"),
		Author:       getString(r, "author"),
		URL:          getString(r, "url"),
		HeadSHA:      getString(r, "head_sha"),
		CreatedAt:    getTime(r, "created_at"),
		UpdatedAt:    getTime(r, "updated_at"),
		QueuePos:     getInt(r, "queue_pos"),
		DiscoveredAt: getTime(r, "discovered_at"),
		Source:       getString(r, "source"),
		WorkDir:      getString(r, "work_dir"),
		HoldReason:   getString(r, "hold_reason"),
		ClaimHost:    getString(r, "claim_host"),
		ClaimPID:     getInt(r, "claim_pid"),
	}
	if t := getTime(r, "claimed_at"); !t.IsZero() {
		c.ClaimedAt = &t
	}
	if t := getTime(r, "eligible_at"); !t.IsZero() {
		c.EligibleAt = &t
	}
	return c
}

// q renders a SQL string literal (single quotes doubled). NULL for empty.
func q(s string) string {
	if s == "" {
		return "NULL"
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// ts renders a TIMESTAMP literal in UTC, or NULL for the zero time.
func ts(t time.Time) string {
	if t.IsZero() {
		return "NULL"
	}
	return "'" + t.UTC().Format("2006-01-02 15:04:05") + "'"
}

// tsp is ts for optional timestamps: NULL for nil.
func tsp(t *time.Time) string {
	if t == nil {
		return "NULL"
	}
	return ts(*t)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func getString(r map[string]any, key string) string {
	v, ok := r[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func getInt(r map[string]any, key string) int {
	v, ok := r[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case string:
		i, _ := strconv.Atoi(n)
		return i
	default:
		return 0
	}
}

func getTime(r map[string]any, key string) time.Time {
	s := getString(r, key)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999", "2006-01-02 15:04:05", time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
