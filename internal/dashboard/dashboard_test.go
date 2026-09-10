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
func ptr[T any](v T) *T { return &v }

func TestViewQueue(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	staleAfter := 2 * time.Hour
	fresh := now.Add(-time.Hour)
	boundary := now.Add(-staleAfter) // exactly one window old, still reviewing
	stale := now.Add(-3 * time.Hour)
	holdUntil := now.Add(30 * time.Minute)
	holdOver := now.Add(-time.Minute)
	in := []store.Candidate{
		{Number: 1},                       // unclaimed
		{Number: 2, ClaimedAt: &fresh},    // engine on it right now
		{Number: 3, ClaimedAt: &stale},    // abandoned lease: next cycle reclaims
		{Number: 4, ClaimedAt: &boundary}, // boundary: must agree with the scheduler
		{Number: 5, Holds: map[string]time.Time{store.HoldCooldown: holdUntil}}, // eligibility hold: visible but skipped
		{Number: 6, Holds: map[string]time.Time{store.HoldCooldown: holdOver}},  // expired hold: plain queued again
		// Two live holds: the later one decides what the row reports, which is
		// the whole contract the frontend renders.
		{Number: 7, Holds: map[string]time.Time{
			store.HoldCooldown: holdUntil, store.HoldEditing: holdUntil.Add(time.Hour),
		}},
		// A mark alongside an expired hold: neither defers, so the row is
		// plainly queued and must project no hold at all.
		{Number: 8, Holds: map[string]time.Time{
			store.HoldCooldown: holdOver, store.MarkEditingSince: now.Add(-time.Hour),
		}},
	}
	got := viewQueue(in, now, staleAfter, viewer{})
	want := []struct {
		status     string
		eligibleAt *time.Time
		reason     string
	}{
		{status: "queued"},
		{status: "reviewing"},
		{status: "queued"},
		{status: "reviewing"},
		{status: "held", eligibleAt: &holdUntil, reason: store.HoldCooldown},
		// An expired hold must not render as a live one: the projection is
		// derived only while the row is actually held.
		{status: "queued"},
		{status: "held", eligibleAt: ptr(holdUntil.Add(time.Hour)), reason: store.HoldEditing},
		{status: "queued"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Status != w.status {
			t.Errorf("row %d (#%d) status = %q, want %q", i, g.Number, g.Status, w.status)
		}
		if g.HoldReason != w.reason {
			t.Errorf("row %d (#%d) hold_reason = %q, want %q", i, g.Number, g.HoldReason, w.reason)
		}
		switch {
		case w.eligibleAt == nil && g.EligibleAt != nil:
			t.Errorf("row %d (#%d) eligible_at = %v, want none", i, g.Number, g.EligibleAt)
		case w.eligibleAt != nil && (g.EligibleAt == nil || !g.EligibleAt.Equal(*w.eligibleAt)):
			t.Errorf("row %d (#%d) eligible_at = %v, want %v", i, g.Number, g.EligibleAt, *w.eligibleAt)
		}
	}
	if empty := viewQueue(nil, now, staleAfter, viewer{}); empty == nil || len(empty) != 0 {
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
		{Number: 4, Holds: map[string]time.Time{store.HoldCooldown: holdUntil}},
	}, now, lease, viewer{})
	got := countQueue(views)
	if got.Total != 4 || got.Queued != 2 || got.Reviewing != 1 || got.Held != 1 {
		t.Errorf("counts = %+v, want total 4 / queued 2 / reviewing 1 / held 1", got)
	}
	if got.Queued+got.Reviewing+got.Held != got.Total {
		t.Errorf("counts must sum to total, got %+v", got)
	}
}

// TestViewQueueCarriesSteering: steering rides on the candidate, so the view
// passes it through untouched. It used to be joined from a second table here,
// which is the coupling the move onto the queue row removed.
func TestViewQueueCarriesSteering(t *testing.T) {
	now := time.Now()
	views := viewQueue([]store.Candidate{
		{Repo: "o/r", Number: 1, HeadSHA: "s1"},
		{Repo: "o/r", Number: 2, HeadSHA: "s2", Steering: &store.Steering{Message: "focus on rollback", SetBy: "octocat"}},
	}, now, 2*time.Hour, viewer{})

	if views[0].Steering != nil {
		t.Errorf("PR 1 has no steering, got %+v", views[0].Steering)
	}
	if views[1].Steering == nil || views[1].Steering.SetBy != "octocat" {
		t.Errorf("PR 2 must carry its steering, got %+v", views[1].Steering)
	}
}

// TestViewQueueMaySteer: the queue tells the UI which rows this viewer may
// steer, decided by the same rule the write path enforces. Without it the
// component reimplemented the rule in TypeScript, where nothing bound the two
// together and a drift showed up as a control that 403s.
func TestViewQueueMaySteer(t *testing.T) {
	now := time.Now()
	rows := []store.Candidate{
		{Repo: "o/r", Number: 1, Author: "octocat"},
		{Repo: "o/r", Number: 2, Author: "someone-else"},
	}
	steerable := func(v viewer) []bool {
		out := []bool{}
		for _, view := range viewQueue(rows, now, 2*time.Hour, v) {
			out = append(out, view.MaySteer)
		}
		return out
	}

	for name, tc := range map[string]struct {
		v    viewer
		want []bool
	}{
		"anonymous":            {viewer{}, []bool{false, false}},
		"identified, unmapped": {viewer{Login: "x@e.com"}, []bool{false, false}},
		"the author":           {viewer{Login: "o@e.com", Handle: "octocat"}, []bool{true, false}},
		"author, cased oddly":  {viewer{Login: "o@e.com", Handle: "OctoCat"}, []bool{true, false}},
		"the gh user":          {viewer{Login: "p@e.com", Handle: "paul-gh", IsGH: true}, []bool{true, true}},
	} {
		got := steerable(tc.v)
		if got[0] != tc.want[0] || got[1] != tc.want[1] {
			t.Errorf("%s: may_steer = %v, want %v", name, got, tc.want)
		}
	}
}
