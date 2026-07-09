package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeDuckDB(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "duckdb")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseNDJSON(t *testing.T) {
	rows, err := parseNDJSON("\n{\n{\"repo\":\"o/r\",\"number\":7}\n{\"repo\":\"x/y\",\"number\":8}\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["repo"] != "o/r" || getInt(rows[1], "number") != 8 {
		t.Errorf("rows = %#v", rows)
	}
	if _, err := parseNDJSON("not json\n"); err == nil {
		t.Error("malformed JSON must report a parse error")
	}
}

func TestQueryReturnsDuckDBStderr(t *testing.T) {
	d := &duckDB{bin: fakeDuckDB(t, "echo 'bad SQL' >&2; exit 12"), path: t.TempDir()}
	if _, err := d.query(context.Background(), "BROKEN"); err == nil || err.Error() != "bad SQL" {
		t.Errorf("query error = %v, want DuckDB stderr", err)
	}
}

// q is the only defense between GitHub-controlled strings (PR titles, author
// handles) and the SQL we build by interpolation — pin its behavior hard.
func TestQ(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"empty is NULL (deliberate: empty fields store as SQL NULL)", "", "NULL"},
		{"plain", "alice", "'alice'"},
		{"single quote doubled", "O'Brien", "'O''Brien'"},
		{"multiple quotes", "a'b'c", "'a''b''c'"},
		{"already doubled stays doubled again", "it''s", "'it''''s'"},
		{"newline passes through literally", "line1\nline2", "'line1\nline2'"},
		{"backslash is not an escape in SQL strings", `a\b`, `'a\b'`},
		{"unicode", "– emoji 🦎", "'– emoji 🦎'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := q(tc.in); got != tc.want {
				t.Errorf("q(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

// TestQNeverBreaksOutOfLiteral asserts the invariant that matters: whatever
// the input, the rendered literal contains no lone quote that would terminate
// the string early.
func TestQNeverBreaksOutOfLiteral(t *testing.T) {
	hostile := []string{
		"'; DROP TABLE candidates; --",
		"' OR '1'='1",
		"'||(SELECT 1)||'",
	}
	for _, in := range hostile {
		got := q(in)
		inner := strings.TrimSuffix(strings.TrimPrefix(got, "'"), "'")
		if strings.Contains(strings.ReplaceAll(inner, "''", ""), "'") {
			t.Errorf("q(%q) = %s — lone quote survives inside the literal", in, got)
		}
	}
}

func TestTS(t *testing.T) {
	if got := ts(time.Time{}); got != "NULL" {
		t.Errorf("ts(zero) = %s, want NULL", got)
	}
	// Non-UTC input renders in UTC.
	loc := time.FixedZone("UTC+2", 2*3600)
	in := time.Date(2026, 7, 7, 14, 30, 45, 999_000_000, loc)
	if got := ts(in); got != "'2026-07-07 12:30:45'" {
		t.Errorf("ts(+2h zone) = %s, want '2026-07-07 12:30:45' (UTC, sub-second truncated)", got)
	}
}
