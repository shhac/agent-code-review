package dashboard

import (
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/store"
)

func TestBucketReviews(t *testing.T) {
	start := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	at := func(offset time.Duration) time.Time { return start.Add(offset) }
	reviews := []store.Review{
		{Verdict: "APPROVED", ReviewedAt: at(0)},                             // first bucket, exact boundary
		{Verdict: "COMMENTED", ReviewedAt: at(59 * time.Minute)},             // still first bucket
		{Verdict: "REQUESTED_CHANGES", ReviewedAt: at(1 * time.Hour)},        // second bucket boundary
		{Verdict: "APPROVED", ReviewedAt: at(23*time.Hour + 59*time.Minute)}, // last bucket
		{Verdict: "APPROVED", ReviewedAt: at(24 * time.Hour)},                // out of window (after)
		{Verdict: "APPROVED", ReviewedAt: at(-time.Minute)},                  // out of window (before)
		{Verdict: "SKIPPED", ReviewedAt: at(0)},                              // never counts
		{Verdict: "ERROR", ReviewedAt: at(0)},                                // never counts
	}
	buckets := bucketReviews(reviews, start)
	if len(buckets) != 24 {
		t.Fatalf("got %d buckets, want 24", len(buckets))
	}
	if buckets[0].Approved != 1 || buckets[0].Commented != 1 || buckets[0].RequestedChanges != 0 {
		t.Errorf("bucket 0 = %+v, want 1 approved + 1 commented", buckets[0])
	}
	if buckets[1].RequestedChanges != 1 {
		t.Errorf("bucket 1 = %+v, want 1 requested_changes", buckets[1])
	}
	if buckets[23].Approved != 1 {
		t.Errorf("bucket 23 = %+v, want 1 approved", buckets[23])
	}
	total := 0
	for _, b := range buckets {
		total += b.Approved + b.Commented + b.RequestedChanges
	}
	if total != 4 {
		t.Errorf("total counted = %d, want 4 (out-of-window and SKIPPED/ERROR excluded)", total)
	}
	if buckets[0].Hour != start.Format(time.RFC3339) {
		t.Errorf("bucket 0 hour = %s, want %s", buckets[0].Hour, start.Format(time.RFC3339))
	}
}
