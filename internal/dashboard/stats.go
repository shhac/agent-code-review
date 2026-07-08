package dashboard

// This file is the stats surface: /api/stats aggregates the last 24 hours of
// review outcomes into hourly buckets for the Overview chart.

import (
	"net/http"
	"time"

	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
)

// statsBucket is one hour of review outcomes in the /api/stats response.
type statsBucket struct {
	Hour             string `json:"hour"`
	Approved         int    `json:"approved"`
	Commented        int    `json:"commented"`
	RequestedChanges int    `json:"requested_changes"`
}

// bucketReviews aggregates reviews into 24 hourly buckets starting at start
// (which must be hour-aligned). Reviews outside [start, start+24h) are
// dropped; SKIPPED/ERROR verdicts don't count as outcomes. Pure — the
// hour-index math and verdict mapping are unit-tested directly.
func bucketReviews(reviews []store.Review, start time.Time) []statsBucket {
	buckets := make([]statsBucket, 24)
	for i := range buckets {
		buckets[i].Hour = start.Add(time.Duration(i) * time.Hour).Format(time.RFC3339)
	}
	for _, rv := range reviews {
		at := rv.ReviewedAt.UTC()
		// Duration division truncates toward zero, so a negative sub-hour
		// offset would land in bucket 0 — guard Before() explicitly.
		if at.Before(start) {
			continue
		}
		i := int(at.Sub(start) / time.Hour)
		if i >= 24 {
			continue
		}
		switch rv.Verdict {
		case review.DecisionApproved:
			buckets[i].Approved++
		case review.DecisionCommented:
			buckets[i].Commented++
		case review.DecisionRequestedChanges:
			buckets[i].RequestedChanges++
		}
	}
	return buckets
}

// handleStats returns 24 hourly buckets of review outcomes for the sliding
// last-24h window: approved / commented / requested_changes counts per hour.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()
	start := time.Now().UTC().Truncate(time.Hour).Add(-23 * time.Hour)
	reviews, err := s.store.ListReviewsSince(ctx, start)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"buckets": bucketReviews(reviews, start)})
}
