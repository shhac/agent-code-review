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

// --- candidates ---

func (d *duckDB) UpsertCandidate(ctx context.Context, c Candidate) error {
	sql := fmt.Sprintf(`INSERT INTO candidates
	  (repo, number, type, title, author, url, head_sha, created_at, updated_at, queue_pos, status, discovered_at)
	VALUES (%s, %d, %s, %s, %s, %s, %s, %s, %s, %d, %s, %s)
	ON CONFLICT (repo, number) DO UPDATE SET
	  type = excluded.type,
	  title = excluded.title,
	  author = excluded.author,
	  url = excluded.url,
	  head_sha = excluded.head_sha,
	  updated_at = excluded.updated_at,
	  discovered_at = excluded.discovered_at`,
		q(c.Repo), c.Number, q(c.Type), q(c.Title), q(c.Author), q(c.URL), q(c.HeadSHA),
		ts(c.CreatedAt), ts(c.UpdatedAt), c.QueuePos, q(orDefault(c.Status, StatusQueued)), ts(c.DiscoveredAt))
	_, err := d.query(ctx, sql)
	return err
}

func (d *duckDB) Requeue(ctx context.Context, c Candidate) error {
	sql := fmt.Sprintf(`INSERT INTO candidates
	  (repo, number, type, title, author, url, head_sha, created_at, updated_at, queue_pos, status, discovered_at)
	VALUES (%s, %d, %s, %s, %s, %s, %s, %s, %s, %d, 'queued', %s)
	ON CONFLICT (repo, number) DO UPDATE SET status = 'queued'`,
		q(c.Repo), c.Number, q(orDefault(c.Type, TypeNew)), q(c.Title), q(c.Author), q(c.URL), q(c.HeadSHA),
		ts(c.CreatedAt), ts(c.UpdatedAt), c.QueuePos, ts(c.DiscoveredAt))
	_, err := d.query(ctx, sql)
	return err
}

func (d *duckDB) ListCandidates(ctx context.Context, f Filter) ([]Candidate, error) {
	var where []string
	if f.Status != "" {
		where = append(where, "status = "+q(f.Status))
	}
	if f.Repo != "" {
		where = append(where, "repo = "+q(f.Repo))
	}
	sql := "SELECT * FROM candidates"
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	// Manual queue positions win outright; among the default 0s the schedule
	// spec's order holds: New before Refreshed, then oldest PR first.
	sql += " ORDER BY queue_pos, CASE type WHEN 'new' THEN 0 ELSE 1 END, number"
	rows, err := d.query(ctx, sql)
	if err != nil {
		return nil, err
	}
	cands := make([]Candidate, 0, len(rows))
	for _, r := range rows {
		cands = append(cands, scanCandidate(r))
	}
	return cands, nil
}

func (d *duckDB) GetCandidate(ctx context.Context, repo string, number int) (Candidate, bool, error) {
	rows, err := d.query(ctx, fmt.Sprintf("SELECT * FROM candidates WHERE repo = %s AND number = %d", q(repo), number))
	if err != nil || len(rows) == 0 {
		return Candidate{}, false, err
	}
	return scanCandidate(rows[0]), true, nil
}

func (d *duckDB) SetStatus(ctx context.Context, repo string, number int, status string) error {
	_, err := d.query(ctx, fmt.Sprintf("UPDATE candidates SET status = %s WHERE repo = %s AND number = %d", q(status), q(repo), number))
	return err
}

func (d *duckDB) SetQueuePos(ctx context.Context, repo string, number int, pos int) error {
	_, err := d.query(ctx, fmt.Sprintf("UPDATE candidates SET queue_pos = %d WHERE repo = %s AND number = %d", pos, q(repo), number))
	return err
}

func (d *duckDB) RemoveCandidate(ctx context.Context, repo string, number int) error {
	_, err := d.query(ctx, fmt.Sprintf("DELETE FROM candidates WHERE repo = %s AND number = %d", q(repo), number))
	return err
}

// --- reviews ---

func (d *duckDB) LastReview(ctx context.Context, repo string, number int) (Review, bool, error) {
	rows, err := d.query(ctx, fmt.Sprintf(
		"SELECT * FROM reviews WHERE repo = %s AND number = %d ORDER BY reviewed_at DESC LIMIT 1", q(repo), number))
	if err != nil || len(rows) == 0 {
		return Review{}, false, err
	}
	return scanReview(rows[0]), true, nil
}

func (d *duckDB) RecordReview(ctx context.Context, r Review) error {
	sql := fmt.Sprintf(
		"INSERT INTO reviews (repo, number, head_sha, verdict, engine, reviewed_at) VALUES (%s, %d, %s, %s, %s, %s)",
		q(r.Repo), r.Number, q(r.HeadSHA), q(r.Verdict), q(r.Engine), ts(r.ReviewedAt))
	_, err := d.query(ctx, sql)
	return err
}

func (d *duckDB) ListReviews(ctx context.Context, limit int) ([]Review, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.query(ctx, fmt.Sprintf(
		"SELECT * FROM reviews ORDER BY reviewed_at DESC LIMIT %d", limit))
	if err != nil {
		return nil, err
	}
	reviews := make([]Review, 0, len(rows))
	for _, r := range rows {
		reviews = append(reviews, scanReview(r))
	}
	return reviews, nil
}

func (d *duckDB) ListReviewsSince(ctx context.Context, since time.Time) ([]Review, error) {
	rows, err := d.query(ctx, fmt.Sprintf(
		"SELECT * FROM reviews WHERE reviewed_at >= %s ORDER BY reviewed_at", ts(since)))
	if err != nil {
		return nil, err
	}
	reviews := make([]Review, 0, len(rows))
	for _, r := range rows {
		reviews = append(reviews, scanReview(r))
	}
	return reviews, nil
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
	runs := make([]Run, 0, len(rows))
	for _, r := range rows {
		runs = append(runs, scanRun(r))
	}
	return runs, nil
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
	sql += " ORDER BY repo, github_handle"
	rows, err := d.query(ctx, sql)
	if err != nil {
		return nil, err
	}
	authors := make([]AllowedAuthor, 0, len(rows))
	for _, r := range rows {
		authors = append(authors, AllowedAuthor{
			Repo:         getString(r, "repo"),
			GitHubHandle: getString(r, "github_handle"),
			Name:         getString(r, "name"),
			Email:        getString(r, "email"),
			SlackID:      getString(r, "slack_id"),
		})
	}
	return authors, nil
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
		Repo:       getString(r, "repo"),
		Number:     getInt(r, "number"),
		HeadSHA:    getString(r, "head_sha"),
		Verdict:    getString(r, "verdict"),
		Engine:     getString(r, "engine"),
		ReviewedAt: getTime(r, "reviewed_at"),
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
	return Candidate{
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
		Status:       getString(r, "status"),
		DiscoveredAt: getTime(r, "discovered_at"),
	}
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
