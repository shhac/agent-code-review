package scheduler

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
)

// TestReconcile pins the crash-recovery boundary: only THIS host's dead-pid
// leftovers are released; live pids (a sibling instance mid-review) and
// other hosts' state are untouched.
func TestReconcile(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	fs := &fakeDispatchStore{
		queue: []store.Candidate{
			{Repo: "o/r", Number: 1, ClaimedAt: &now, ClaimHost: host, ClaimPID: 111},        // dead → release
			{Repo: "o/r", Number: 2, ClaimedAt: &now, ClaimHost: host, ClaimPID: 222},        // alive → keep
			{Repo: "o/r", Number: 3, ClaimedAt: &now, ClaimHost: "elsewhere", ClaimPID: 111}, // other host → keep
			{Repo: "o/r", Number: 4}, // unclaimed → keep
			{Repo: "o/r", Number: 5, ClaimedAt: &now, ClaimHost: host, ClaimPID: 0}, // pre-tracking pid 0... host matches, pid dead → release
		},
	}
	s := New(Deps{Store: fs, Config: func() config.Config { return config.Config{} }, GHUser: "u"})
	s.pidAlive = func(pid int) bool { return pid == 222 }

	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Every released claim leaves a record. Without one the interruption is
	// invisible: no history row, and a work_dir the next claim overwrites, so
	// the transcript of a review that may have spent minutes and dollars is
	// unreachable.
	if len(fs.abandoned) != 2 {
		t.Fatalf("abandoned records = %d, want one per released claim", len(fs.abandoned))
	}
	for _, r := range fs.abandoned {
		// ERROR is deliberately not a "real" verdict, so the abandoned attempt
		// cannot pass for a review that happened and the PR stays eligible.
		if r.Verdict != store.VerdictError || store.IsRealVerdict(r.Verdict) {
			t.Errorf("abandoned record = %q, want a non-real ERROR verdict", r.Verdict)
		}
		if r.Engine != store.EngineAbandoned {
			t.Errorf("engine = %q, want %q rather than a guess at which engine ran", r.Engine, store.EngineAbandoned)
		}
	}
	if len(fs.cleared) != 2 || fs.cleared[0] != 1 || fs.cleared[1] != 5 {
		t.Errorf("cleared claims = %v, want [1 5]", fs.cleared)
	}
}
