package scheduler

import (
	"testing"

	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
)

// These two mappings guard the Refreshed-detection invariant: only real
// reviews may be recorded at a SHA, or skips/failures would permanently
// suppress re-detection of a PR.
func TestVerdictMappings(t *testing.T) {
	cases := []struct {
		decision   string
		realReview bool
		status     string
	}{
		{review.DecisionApproved, true, store.StatusReviewed},
		{review.DecisionCommented, true, store.StatusReviewed},
		{review.DecisionRequestedChanges, true, store.StatusReviewed},
		{review.DecisionSkipped, false, store.StatusSkipped},
		{review.DecisionError, false, store.StatusError},
		{"", false, store.StatusError},
		{"GARBAGE", false, store.StatusError},
	}
	for _, tc := range cases {
		t.Run("decision="+tc.decision, func(t *testing.T) {
			if got := isActualReview(tc.decision); got != tc.realReview {
				t.Errorf("isActualReview(%q) = %v, want %v", tc.decision, got, tc.realReview)
			}
			if got := statusFor(tc.decision); got != tc.status {
				t.Errorf("statusFor(%q) = %q, want %q", tc.decision, got, tc.status)
			}
		})
	}
}
