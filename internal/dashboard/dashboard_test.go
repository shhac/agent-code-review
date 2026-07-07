package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
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

func TestValidateReorder(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	staleAfter := 2 * time.Hour
	fresh := now.Add(-time.Hour)
	queue := []store.Candidate{
		{Repo: "example-org/service-alpha", Number: 1, ClaimedAt: &fresh}, // reviewing: pinned
		{Repo: "example-org/service-alpha", Number: 2},
		{Repo: "example-org/service-beta", Number: 3},
	}
	ref := func(repo string, n int) prRef { return prRef{Repo: repo, Number: n} }

	cases := []struct {
		name    string
		order   []prRef
		wantErr string // substring; empty = valid
	}{
		{"full queued set in new order", []prRef{ref("example-org/service-beta", 3), ref("example-org/service-alpha", 2)}, ""},
		{"reviewing row cannot be reordered", []prRef{ref("example-org/service-alpha", 1), ref("example-org/service-alpha", 2)}, "not reorderable"},
		{"unknown PR rejected", []prRef{ref("example-org/service-alpha", 2), ref("example-org/ghost", 99)}, "not reorderable"},
		{"duplicate rejected", []prRef{ref("example-org/service-alpha", 2), ref("example-org/service-alpha", 2)}, "twice"},
		{"incomplete order rejected", []prRef{ref("example-org/service-alpha", 2)}, "exactly once"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateReorder(queue, tc.order, now, staleAfter)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}
func TestPRRefPattern(t *testing.T) {
	cases := []struct {
		in         string
		repo       string
		number     string
		shouldFail bool
	}{
		{"https://github.com/owner/repo/pull/123", "owner/repo", "123", false},
		{"https://github.com/owner/repo/pull/123/files", "owner/repo", "123", false},
		{"owner/repo/pull/9", "owner/repo", "9", false},
		{"owner/my.repo-x_1/pull/42", "owner/my.repo-x_1", "42", false},
		{"http://github.com/owner/repo/pull/1", "", "", true},  // https only
		{"https://gitlab.com/owner/repo/pull/1", "", "", true}, // github.com only
		{"owner/repo#123", "", "", true},
		{"owner/repo", "", "", true},
		{"just words", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			m := prRefPattern.FindStringSubmatch(tc.in)
			if tc.shouldFail {
				if m != nil {
					t.Errorf("expected no match, got %v", m)
				}
				return
			}
			if m == nil {
				t.Fatal("expected match, got none")
			}
			if m[1] != tc.repo || m[2] != tc.number {
				t.Errorf("got repo=%s number=%s, want %s %s", m[1], m[2], tc.repo, tc.number)
			}
		})
	}
}

func TestRepoWatched(t *testing.T) {
	s := &Server{config: func() config.Config {
		return config.Config{Repos: []string{"Org/Repo-One", "org/two"}}
	}}
	if !s.repoWatched("org/repo-one") {
		t.Error("matching must be case-insensitive (GitHub semantics)")
	}
	if !s.repoWatched("ORG/TWO") {
		t.Error("upper-case variant must match")
	}
	if s.repoWatched("org/other") {
		t.Error("unwatched repo must not match")
	}
	empty := &Server{config: func() config.Config { return config.Config{} }}
	if empty.repoWatched("any/repo") {
		t.Error("no watched repos means nothing is accepted")
	}
}

func TestViewQueue(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	staleAfter := 2 * time.Hour
	fresh := now.Add(-time.Hour)
	boundary := now.Add(-staleAfter) // exactly one window old — still reviewing
	stale := now.Add(-3 * time.Hour)
	in := []store.Candidate{
		{Number: 1},                       // unclaimed
		{Number: 2, ClaimedAt: &fresh},    // engine on it right now
		{Number: 3, ClaimedAt: &stale},    // abandoned lease — next cycle reclaims
		{Number: 4, ClaimedAt: &boundary}, // boundary — must agree with the scheduler
	}
	got := viewQueue(in, now, staleAfter)
	want := []string{"queued", "reviewing", "queued", "reviewing"}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i, status := range want {
		if got[i].Status != status {
			t.Errorf("row %d (#%d) status = %q, want %q", i, got[i].Number, got[i].Status, status)
		}
	}
	if empty := viewQueue(nil, now, staleAfter); empty == nil || len(empty) != 0 {
		t.Errorf("nil input must return a non-nil empty slice, got %#v", empty)
	}
}

func TestQueryInt(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"", 50}, // absent → default
		{"?limit=25", 25},
		{"?limit=500", 500}, // inclusive upper bound
		{"?limit=501", 50},  // over max → default
		{"?limit=0", 50},    // zero → default
		{"?limit=-3", 50},   // negative → default
		{"?limit=abc", 50},  // garbage → default
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodGet, "/api/reviews"+tc.raw, nil)
		if got := queryInt(r, "limit", 50, 500); got != tc.want {
			t.Errorf("queryInt(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}
