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
	"strings"
	"sync"
)

//go:embed schema.sql
var schemaSQL string

// duckDB is a subprocess-backed Store: every statement spawns a `duckdb` CLI
// process against the same file, exactly like agent-sql's driver. A mutex
// serializes access because DuckDB is single-writer per file and the daemon
// reviews up to N PRs concurrently.
//
// readOnly runs every statement with the CLI's -readonly flag: since each
// statement is its own short-lived process, a read-only reader can safely
// inspect the file alongside the live daemon, and any write is refused by
// DuckDB itself rather than needing per-method guards.
type duckDB struct {
	bin      string
	path     string
	readOnly bool
	mu       sync.Mutex
}

func newDuckDB(path string, readOnly bool) (*duckDB, error) {
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
	return &duckDB{bin: bin, path: path, readOnly: readOnly}, nil
}

func (d *duckDB) Init(ctx context.Context) error {
	// Applying the schema is a write; a read-only store attaches to an existing
	// DB, so validate reachability with a trivial read instead.
	if d.readOnly {
		return d.exec(ctx, "SELECT 1")
	}
	return d.exec(ctx, schemaSQL)
}

func (d *duckDB) Close() error { return nil }

// --- exec plumbing (mirrors agent-sql/internal/driver/duckdb) ---

func (d *duckDB) query(ctx context.Context, sql string) ([]map[string]any, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	args := []string{"-cmd", ".mode jsonlines"}
	if d.readOnly {
		args = append(args, "-readonly")
	}
	args = append(args, d.path, "-c", sql)
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

// exec runs a statement whose result rows are deliberately ignored. Keeping
// that intent at the call site separates mutations from queries, while query
// remains available for the few writes (such as Claim) that use RETURNING.
func (d *duckDB) exec(ctx context.Context, sql string) error {
	_, err := d.query(ctx, sql)
	return err
}

func queryOne[T any](ctx context.Context, d *duckDB, sql string, scan func(map[string]any) T) (T, bool, error) {
	rows, err := d.query(ctx, sql)
	var zero T
	if err != nil || len(rows) == 0 {
		return zero, false, err
	}
	return scan(rows[0]), true, nil
}

// queryMany is the shared tail of all List* methods: every result row goes
// through one scanner, and the preallocated non-nil slice keeps the
// empty-result contract ([] not null after JSON encoding).
func queryMany[T any](ctx context.Context, d *duckDB, sql string, scan func(map[string]any) T) ([]T, error) {
	rows, err := d.query(ctx, sql)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(rows))
	for _, r := range rows {
		out = append(out, scan(r))
	}
	return out, nil
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
