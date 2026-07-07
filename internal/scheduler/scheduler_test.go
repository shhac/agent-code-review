package scheduler

import (
	"testing"

	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
)

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
