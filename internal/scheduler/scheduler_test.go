package scheduler

import (
	"testing"
	"time"

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
