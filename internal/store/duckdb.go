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

func queryOne[T any](ctx context.Context, d *duckDB, sql string, scan func(map[string]any) T) (T, bool, error) {
	rows, err := d.query(ctx, sql)
	var zero T
	if err != nil || len(rows) == 0 {
		return zero, false, err
	}
	return scan(rows[0]), true, nil
}

func queryMany[T any](ctx context.Context, d *duckDB, sql string, scan func(map[string]any) T) ([]T, error) {
	rows, err := d.query(ctx, sql)
	if err != nil {
		return nil, err
	}
	return mapRows(rows, scan), nil
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
