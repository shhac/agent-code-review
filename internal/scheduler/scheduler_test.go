package scheduler

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
)

// TestDue pins the heartbeat's firing rule, including the live-reload
// property: a cadence shrunk below the already-elapsed time makes the run
// due on the next beat, without waiting out the old interval.
func TestDue(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		elapsed  time.Duration
		interval time.Duration
		want     bool
	}{
		{"just under the interval", 14 * time.Minute, 15 * time.Minute, false},
		{"exactly at the interval", 15 * time.Minute, 15 * time.Minute, true},
		{"past the interval", 16 * time.Minute, 15 * time.Minute, true},
		{"interval shrunk below elapsed", 20 * time.Minute, 15 * time.Minute, true},
		{"interval grown above elapsed", 20 * time.Minute, 90 * time.Minute, false},
	}
	for _, tc := range cases {
		if got := due(now.Add(-tc.elapsed), now, tc.interval); got != tc.want {
			t.Errorf("%s: due = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Guards the Refreshed-detection invariant: exactly the engine's real-review
// decisions count as "reviewed at this SHA" (store.LastReview filters on
// this), while SKIPPED/ERROR outcomes stay re-surfaceable.
func TestRealVerdictMapping(t *testing.T) {
	cases := []struct {
		decision   string
		realReview bool
	}{
		{review.DecisionApproved, true},
		{review.DecisionCommented, true},
		{review.DecisionRequestedChanges, true},
		{review.DecisionSkipped, false},
		{review.DecisionError, false},
		{"", false},
		{"GARBAGE", false},
	}
	for _, tc := range cases {
		if got := store.IsRealVerdict(tc.decision); got != tc.realReview {
			t.Errorf("IsRealVerdict(%q) = %v, want %v", tc.decision, got, tc.realReview)
		}
	}
}

// reconcileStore fakes the reconciliation surface: claimed queue rows in,
// ClearClaim calls out.
type reconcileStore struct {
	store.Store

	queue     []store.Candidate
	cleared   []int // queue row numbers whose claims were cleared
	abandoned []store.Review
}

func (f *reconcileStore) AppendHistory(_ context.Context, r store.Review) error {
	f.abandoned = append(f.abandoned, r)
	return nil
}

func (f *reconcileStore) ListQueue(context.Context, string) ([]store.Candidate, error) {
	return f.queue, nil
}
func (f *reconcileStore) ClearClaim(_ context.Context, _ string, number int) error {
	f.cleared = append(f.cleared, number)
	return nil
}

// TestReconcile pins the crash-recovery boundary: only THIS host's dead-pid
// leftovers are released; live pids (a sibling instance mid-review) and
// other hosts' state are untouched.
func TestReconcile(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	fs := &reconcileStore{
		queue: []store.Candidate{
			{Repo: "o/r", Number: 1, ClaimedAt: &now, ClaimHost: host, ClaimPID: 111},        // dead → release
			{Repo: "o/r", Number: 2, ClaimedAt: &now, ClaimHost: host, ClaimPID: 222},        // alive → keep
			{Repo: "o/r", Number: 3, ClaimedAt: &now, ClaimHost: "elsewhere", ClaimPID: 111}, // other host → keep
			{Repo: "o/r", Number: 4}, // unclaimed → keep
			{Repo: "o/r", Number: 5, ClaimedAt: &now, ClaimHost: host, ClaimPID: 0}, // pre-tracking pid 0... host matches, pid dead → release
		},
	}
	s := New(func() config.Config { return config.Config{} }, fs, nil, "u", nil, nil)
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
