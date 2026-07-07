package store

import (
	"testing"
	"time"
)

// TestClaimActive pins the lease predicate both the scheduler (reclaim) and
// the dashboard ("reviewing" badge) are defined in terms of — including the
// exact-boundary case: a claim aged exactly one window is still live.
func TestClaimActive(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	window := 2 * time.Hour
	at := func(age time.Duration) *time.Time {
		v := now.Add(-age)
		return &v
	}
	cases := []struct {
		name string
		c    Candidate
		want bool
	}{
		{"unclaimed", Candidate{}, false},
		{"fresh claim", Candidate{ClaimedAt: at(time.Hour)}, true},
		{"exactly one window old is still live", Candidate{ClaimedAt: at(window)}, true},
		{"just past the window is stale", Candidate{ClaimedAt: at(window + time.Second)}, false},
	}
	for _, tc := range cases {
		if got := tc.c.ClaimActive(now, window); got != tc.want {
			t.Errorf("%s: ClaimActive = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestRealVerdictsSQLDerivation: the SQL filter literal must be generated
// from the same list the Go predicate uses.
func TestRealVerdictsSQLDerivation(t *testing.T) {
	want := "('APPROVED', 'COMMENTED', 'REQUESTED_CHANGES')"
	if realVerdictsSQL != want {
		t.Errorf("realVerdictsSQL = %q, want %q", realVerdictsSQL, want)
	}
	for _, v := range realVerdicts {
		if !IsRealVerdict(v) {
			t.Errorf("IsRealVerdict(%q) must be true for every listed verdict", v)
		}
	}
}
