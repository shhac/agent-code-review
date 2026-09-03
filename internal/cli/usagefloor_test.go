package cli

import (
	"errors"
	"sync"
	"testing"

	"github.com/shhac/agent-code-review/internal/usage"
)

// TestOneShotUsageProbesOncePerEngine pins the money path. `run` used to skip
// the usage floor entirely, which was survivable only because a global
// run-lock made a cron run overlapping a live daemon a no-op. Without that
// lock, a scheduled run would drain the queue with the floor disabled at
// exactly the moment the daemon had parked itself at that floor. These are the
// properties that stop it: probe once, cache, and fail open.
func TestOneShotUsageProbesOncePerEngine(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	snap := usage.Snapshot{Plan: "pro"}

	get := oneShotUsage(func(engine string) (usage.Snapshot, error) {
		mu.Lock()
		defer mu.Unlock()
		calls[engine]++
		return snap, nil
	})

	if got := get("codex"); got.Plan != "pro" {
		t.Fatalf("first probe returned %+v, want the fetched snapshot", got)
	}
	if got := get("codex"); got.Plan != "pro" {
		t.Fatalf("cached probe returned %+v, want the fetched snapshot", got)
	}
	get("claude")

	mu.Lock()
	defer mu.Unlock()
	if calls["codex"] != 1 {
		t.Errorf("codex probed %d times, want 1: a one-shot run must not re-probe per candidate", calls["codex"])
	}
	if calls["claude"] != 1 {
		t.Errorf("claude probed %d times, want 1", calls["claude"])
	}
}

// TestOneShotUsageFailsOpen: a broken or logged-out engine must degrade to
// reviewing, not to a run that silently does nothing. usage.BelowFloor never
// pauses on an empty snapshot, so caching the zero value is what makes the
// failure open rather than closed — and the error must not be re-probed on
// every candidate.
func TestOneShotUsageFailsOpen(t *testing.T) {
	calls := 0
	get := oneShotUsage(func(string) (usage.Snapshot, error) {
		calls++
		return usage.Snapshot{Plan: "leaked"}, errors.New("codex not logged in")
	})

	got := get("codex")
	if got.Plan != "" {
		t.Errorf("a failed probe must cache the EMPTY snapshot, got %+v", got)
	}
	if paused, _ := usage.BelowFloor(got, 10, 10); paused {
		t.Error("an empty snapshot must not pause reviews: the floor fails open")
	}

	get("codex")
	if calls != 1 {
		t.Errorf("the failed probe ran %d times, want 1: the failure must be cached too", calls)
	}
}
