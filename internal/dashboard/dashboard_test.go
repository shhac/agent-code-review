package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/prref"
	"github.com/shhac/agent-code-review/internal/store"
)

func TestValidateReorder(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	staleAfter := 2 * time.Hour
	fresh := now.Add(-time.Hour)
	queue := []store.Candidate{
		{Repo: "example-org/service-alpha", Number: 1, ClaimedAt: &fresh}, // reviewing: pinned
		{Repo: "example-org/service-alpha", Number: 2},
		{Repo: "example-org/service-beta", Number: 3},
	}
	ref := func(repo string, n int) prref.Ref { return prref.Ref{Repo: repo, Number: n} }

	cases := []struct {
		name    string
		order   []prref.Ref
		wantErr string // substring; empty = valid
	}{
		{"full queued set in new order", []prref.Ref{ref("example-org/service-beta", 3), ref("example-org/service-alpha", 2)}, ""},
		{"reviewing row cannot be reordered", []prref.Ref{ref("example-org/service-alpha", 1), ref("example-org/service-alpha", 2)}, "not reorderable"},
		{"unknown PR rejected", []prref.Ref{ref("example-org/service-alpha", 2), ref("example-org/ghost", 99)}, "not reorderable"},
		{"duplicate rejected", []prref.Ref{ref("example-org/service-alpha", 2), ref("example-org/service-alpha", 2)}, "twice"},
		{"incomplete order rejected", []prref.Ref{ref("example-org/service-alpha", 2)}, "exactly once"},
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
func TestViewQueue(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	staleAfter := 2 * time.Hour
	fresh := now.Add(-time.Hour)
	boundary := now.Add(-staleAfter) // exactly one window old, still reviewing
	stale := now.Add(-3 * time.Hour)
	holdUntil := now.Add(30 * time.Minute)
	holdOver := now.Add(-time.Minute)
	in := []store.Candidate{
		{Number: 1},                         // unclaimed
		{Number: 2, ClaimedAt: &fresh},      // engine on it right now
		{Number: 3, ClaimedAt: &stale},      // abandoned lease: next cycle reclaims
		{Number: 4, ClaimedAt: &boundary},   // boundary: must agree with the scheduler
		{Number: 5, EligibleAt: &holdUntil}, // eligibility hold: visible but skipped
		{Number: 6, EligibleAt: &holdOver},  // expired hold: plain queued again
	}
	got := viewQueue(in, now, staleAfter)
	want := []string{"queued", "reviewing", "queued", "reviewing", "held", "queued"}
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

// TestCountQueue keeps the header-badge counts consistent with the per-row
// statuses viewQueue assigns: queued + reviewing + held always sums to total.
func TestCountQueue(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	lease := 2 * time.Hour
	fresh := now.Add(-time.Hour)
	stale := now.Add(-3 * time.Hour)
	holdUntil := now.Add(time.Hour)
	views := viewQueue([]store.Candidate{
		{Number: 1},
		{Number: 2, ClaimedAt: &fresh},
		{Number: 3, ClaimedAt: &stale},
		{Number: 4, EligibleAt: &holdUntil},
	}, now, lease)
	got := countQueue(views)
	if got.Total != 4 || got.Queued != 2 || got.Reviewing != 1 || got.Held != 1 {
		t.Errorf("counts = %+v, want total 4 / queued 2 / reviewing 1 / held 1", got)
	}
	if got.Queued+got.Reviewing+got.Held != got.Total {
		t.Errorf("counts must sum to total, got %+v", got)
	}
}
