package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

const lockConflictStderr = `echo 'IO Error: Could not set lock on file "q.duckdb": Conflicting lock is held in duckdb (PID 1) by user test.' >&2`

// countingDuckDB is a fake CLI that counts its invocations in a file, so a
// test can see how many attempts query actually made. It reports a lock
// conflict on the first `locked` calls and succeeds after that.
func countingDuckDB(t *testing.T, locked int) (bin string, calls func() int) {
	t.Helper()
	counter := filepath.Join(t.TempDir(), "calls")
	bin = fakeDuckDB(t, fmt.Sprintf(`n=$(cat %[1]q 2>/dev/null || echo 0); n=$((n+1)); echo $n > %[1]q
if [ $n -le %[2]d ]; then %[3]s; exit 1; fi
echo '{"n":1}'`, counter, locked, lockConflictStderr))
	return bin, func() int {
		raw, err := os.ReadFile(counter)
		if err != nil {
			t.Fatal(err)
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
}

// A lock that clears inside the budget must be invisible to the caller.
func TestQueryRidesOutABriefLock(t *testing.T) {
	bin, calls := countingDuckDB(t, 2)
	d := &duckDB{bin: bin, path: t.TempDir()}

	rows, err := d.query(t.Context(), "SELECT 1")
	if err != nil {
		t.Fatalf("a lock that clears on the third attempt must not surface: %v", err)
	}
	if len(rows) != 1 || getInt(rows[0], "n") != 1 {
		t.Errorf("rows = %#v, want the successful attempt's result", rows)
	}
	if got := calls(); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
}

// A file that stays locked is genuinely held, and the caller must hear about
// it once the budget is spent rather than wait on it indefinitely.
func TestQueryReportsALockThatOutlastsTheBudget(t *testing.T) {
	bin, calls := countingDuckDB(t, 1<<30)
	d := &duckDB{bin: bin, path: t.TempDir()}

	_, err := d.query(t.Context(), "SELECT 1")
	if err == nil || !isLockConflict(err) {
		t.Fatalf("err = %v, want the last lock conflict", err)
	}
	if got := calls(); got != lockRetries+1 {
		t.Errorf("attempts = %d, want %d", got, lockRetries+1)
	}
}

// A caller that gives up must not be held for the rest of the backoff. The
// fake reports the lock and then blocks, so the cancel always lands after
// the conflict is on stderr and before any retry could start: exec replaces
// the shell so the kill takes the pipe's only writer with it.
func TestQueryStopsBackingOffWhenCancelled(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	bin := fakeDuckDB(t, fmt.Sprintf("%s\n: > %q\nexec sleep 5", lockConflictStderr, started))
	d := &duckDB{bin: bin, path: t.TempDir()}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		for {
			if _, err := os.Stat(started); err == nil {
				cancel()
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	// No wall-clock bound, which flakes on a loaded machine. The error alone
	// proves the backoff saw the cancel: a retry started on a cancelled context
	// fails as "DuckDB query failed", not as ctx.Err().
	_, err := d.query(ctx, "SELECT 1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
