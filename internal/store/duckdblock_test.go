package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestIsLockConflict pins what gets retried. Matching is on the subprocess's
// stderr text, so it has to be tight: retrying a genuine IO error would turn a
// hard failure into a slow hard failure, and failing to match the real one
// leaves the user-visible error this exists to remove.
func TestIsLockConflict(t *testing.T) {
	real := errors.New(`IO Error: Could not set lock on file "/x/queue.duckdb": ` +
		`Conflicting lock is held in /opt/homebrew/Cellar/duckdb/1.5.5/bin/duckdb (PID 70564) by user paul.`)
	if !isLockConflict(real) {
		t.Error("DuckDB's real lock-conflict message must be retried")
	}

	for _, other := range []string{
		"IO Error: Could not set lock on file \"/x/queue.duckdb\": Permission denied",
		"Catalog Error: Table with name queue does not exist!",
		"Conflicting lock is held somewhere unrelated",
		"",
	} {
		if isLockConflict(errors.New(other)) {
			t.Errorf("must not retry: %q", other)
		}
	}
}

// TestLockRetryBudget: the wait has to be short enough that a caller riding
// out a neighbouring statement never notices, and long enough to cover one.
// A statement holds the file for roughly the life of its subprocess (~25ms
// measured), so the budget spans several of those.
func TestLockRetryBudget(t *testing.T) {
	total := lockBackoff * (1 + 2 + 3) // the per-attempt escalation in query
	if total < 250*time.Millisecond {
		t.Errorf("retry budget %s is under a few statement lifetimes", total)
	}
	if total > time.Second {
		t.Errorf("retry budget %s is long enough for a caller to feel it", total)
	}
	if lockRetries < 2 {
		t.Errorf("lockRetries = %d, want at least 2 spaced attempts", lockRetries)
	}
}

// TestQueryReportsANonLockErrorImmediately: a real failure must not be
// retried into a slow failure.
func TestQueryReportsANonLockErrorImmediately(t *testing.T) {
	d := &duckDB{bin: DuckDBBin(), path: "/nonexistent/dir/queue.duckdb"}
	_, err := d.query(t.Context(), "SELECT 1")
	if err == nil {
		t.Fatal("querying an unopenable path must fail")
	}
	if isLockConflict(err) {
		t.Fatalf("this fixture must not produce a lock error: %v", err)
	}
	if strings.Contains(err.Error(), "context") {
		t.Errorf("the failure must be the DuckDB error, not a timeout: %v", err)
	}
}
