package store

import (
	"context"
	"testing"
	"time"
)

// TestClaimActive pins the lease predicate both the scheduler (reclaim) and
// the dashboard ("reviewing" badge) are defined in terms of, including the
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

// TestHeld pins the hold predicate the scheduler's eligibility filter and
// the dashboard's "on hold" badge are defined in terms of, including the
// exact-boundary case: at eligible_at precisely, the hold is over.
func TestHeld(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time {
		v := now.Add(d)
		return &v
	}
	cases := []struct {
		name string
		c    Candidate
		want bool
	}{
		{"no hold", Candidate{}, false},
		{"future eligibility is held", Candidate{EligibleAt: at(time.Minute)}, true},
		{"exactly eligible is not held", Candidate{EligibleAt: at(0)}, false},
		{"expired hold is not held", Candidate{EligibleAt: at(-time.Minute)}, false},
	}
	for _, tc := range cases {
		if got := tc.c.Held(now); got != tc.want {
			t.Errorf("%s: Held = %v, want %v", tc.name, got, tc.want)
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

// TestReviewFromDuration pins the duration contract: a zero started time
// records an honest 0 (manual skips, backfilled rows), never a bogus
// multi-year elapsed from the zero time; a real claim time records the
// elapsed seconds. The identity fan-out must copy through either way.
func TestReviewFromDuration(t *testing.T) {
	c := Candidate{Repo: "o/r", Number: 5, Title: "T", Author: "a", HeadSHA: "sha1", WorkDir: "/wd"}

	skip := ReviewFrom(c, "SKIPPED", EngineManual, time.Time{})
	if skip.DurationSecs != 0 {
		t.Errorf("zero started must record duration 0, got %d", skip.DurationSecs)
	}

	elapsed := ReviewFrom(c, "APPROVED", "codex", time.Now().Add(-90*time.Second))
	if elapsed.DurationSecs < 89 || elapsed.DurationSecs > 92 {
		t.Errorf("duration = %ds, want ~90", elapsed.DurationSecs)
	}
	if elapsed.Repo != "o/r" || elapsed.Number != 5 || elapsed.Title != "T" ||
		elapsed.Author != "a" || elapsed.HeadSHA != "sha1" || elapsed.WorkDir != "/wd" {
		t.Errorf("candidate identity must copy through, got %+v", elapsed)
	}
}

// workspaceStore fakes the two reads FindReviewWorkspace performs; every other
// Store method panics via the embedded nil interface.
type workspaceStore struct {
	Store

	queue  []Candidate
	last   Review
	lastOK bool
	byKey  map[string]Review
}

func (f *workspaceStore) ListQueue(context.Context, string) ([]Candidate, error) {
	return f.queue, nil
}

func (f *workspaceStore) LastOutcome(context.Context, string, int) (Review, bool, error) {
	return f.last, f.lastOK, nil
}

func (f *workspaceStore) ReviewByLogKey(_ context.Context, repo string, number int, key string) (Review, bool, error) {
	r, ok := f.byKey[key]
	return r, ok && r.Repo == repo && r.Number == number, nil
}

// TestFindWorkspace pins the shared queue-then-history resolution behind
// `queue log` and the dashboard's review-log endpoint.
func TestFindWorkspace(t *testing.T) {
	ctx := context.Background()

	t.Run("queued row wins over history", func(t *testing.T) {
		s := &workspaceStore{
			queue:  []Candidate{{Number: 4}, {Number: 5, WorkDir: "/live"}},
			last:   Review{WorkDir: "/old"},
			lastOK: true,
		}
		ws, found, err := FindReviewWorkspace(ctx, s, ReviewLogRef{Repo: "o/r", Number: 5})
		if err != nil || !found {
			t.Fatalf("found=%v err=%v", found, err)
		}
		if ws.Dir != "/live" || ws.Queued == nil || ws.Finished != nil {
			t.Errorf("want the live queue row, got %+v", ws)
		}
	})

	t.Run("queued row without a workdir falls back to history", func(t *testing.T) {
		s := &workspaceStore{
			queue:  []Candidate{{Number: 5}},
			last:   Review{WorkDir: "/old", Verdict: "APPROVED"},
			lastOK: true,
		}
		ws, found, err := FindReviewWorkspace(ctx, s, ReviewLogRef{Repo: "o/r", Number: 5})
		if err != nil || !found {
			t.Fatalf("found=%v err=%v", found, err)
		}
		if ws.Dir != "/old" || ws.Finished == nil || ws.Queued != nil {
			t.Errorf("want the history row, got %+v", ws)
		}
	})

	t.Run("no workspace ever recorded", func(t *testing.T) {
		s := &workspaceStore{last: Review{}, lastOK: true}
		if _, found, err := FindReviewWorkspace(ctx, s, ReviewLogRef{Repo: "o/r", Number: 5}); err != nil || found {
			t.Errorf("pre-feature reviews have no workspace; found=%v err=%v", found, err)
		}
	})
}

func TestFindReviewWorkspaceByLogKey(t *testing.T) {
	ctx := context.Background()
	reviewed := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	chosen := Review{Repo: "o/r", Number: 5, HeadSHA: "sha1", Verdict: "COMMENTED", ReviewedAt: reviewed, WorkDir: "/chosen"}
	chosen.LogKey = ReviewLogKey(chosen)
	s := &workspaceStore{
		queue:  []Candidate{{Number: 5, WorkDir: "/live"}},
		last:   Review{WorkDir: "/latest"},
		lastOK: true,
	}
	s.byKey = map[string]Review{chosen.LogKey: chosen}

	ws, found, err := FindReviewWorkspace(ctx, s, ReviewLogRefFromReview(chosen))
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if ws.Dir != "/chosen" || ws.Finished == nil || ws.Queued != nil {
		t.Errorf("review key must select the exact history row, got %+v", ws)
	}
}
