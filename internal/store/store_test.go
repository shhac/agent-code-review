package store

import (
	"context"
	"strings"
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

// TestFindReviewWorkspace pins the shared queue-then-history resolution behind
// `queue log` and the dashboard's review-log endpoint.
func TestFindReviewWorkspace(t *testing.T) {
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

	ws, found, err := FindReviewWorkspace(ctx, s, ReviewLogRef{Repo: chosen.Repo, Number: chosen.Number, LogKey: ReviewLogKey(chosen)})
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if ws.Dir != "/chosen" || ws.Finished == nil || ws.Queued != nil {
		t.Errorf("review key must select the exact history row, got %+v", ws)
	}
}

// The two quoting helpers exist so a call site states its nullability policy
// instead of inheriting one. That distinction has teeth: a NULL group_name is
// deliberately read back as the approver group for rows written before the
// column existed, so a column that quietly became NULL would hand out
// approve-level policy.
func TestTextAndNullTextDifferOnEmpty(t *testing.T) {
	if got := text(""); got != "''" {
		t.Errorf(`text("") = %s, want an empty string literal`, got)
	}
	if got := nullText(""); got != "NULL" {
		t.Errorf(`nullText("") = %s, want NULL`, got)
	}
	// Both escape embedded quotes; neither may produce an injectable literal.
	const nasty = "o'brien'; DROP TABLE queue; --"
	for name, got := range map[string]string{"text": text(nasty), "nullText": nullText(nasty)} {
		if strings.Count(got, "'")%2 != 0 {
			t.Errorf("%s(%q) = %s: unbalanced quotes", name, nasty, got)
		}
		if !strings.Contains(got, "o''brien''") {
			t.Errorf("%s(%q) = %s: embedded quotes not doubled", name, nasty, got)
		}
	}
}

// Scanners used to swallow a value they could not interpret and hand back a
// zero, so a renamed or retyped column read as legitimate data: a review with
// no tokens, a run at the zero instant. That is the worst shape a storage bug
// can take, because nothing anywhere reports it.
//
// The distinction that keeps this from being noisy: an ABSENT column is not
// drift. Queries select subsets and several columns are genuinely optional, so
// absent still yields the zero value.
func TestScannersReportDriftButTolerateAbsentColumns(t *testing.T) {
	t.Run("a column that is present but unreadable is an error", func(t *testing.T) {
		for name, values := range map[string]map[string]any{
			"int":       {"repo": "o/r", "number": "not-a-number"},
			"float":     {"repo": "o/r", "cost_usd": "not-a-float"},
			"timestamp": {"repo": "o/r", "reviewed_at": "the day before yesterday"},
			"type":      {"repo": "o/r", "number": []any{1, 2}},
		} {
			t.Run(name, func(t *testing.T) {
				if _, err := scanReview(values); err == nil {
					t.Errorf("an uninterpretable %s column must be reported, not read as zero", name)
				}
			})
		}
	})

	t.Run("absent and null columns are not drift", func(t *testing.T) {
		// The shape a narrow SELECT produces: a few columns, the rest absent.
		got, err := scanReview(map[string]any{"repo": "o/r", "number": float64(7), "usage_raw": nil})
		if err != nil {
			t.Fatalf("a narrow select must scan cleanly, got %v", err)
		}
		if got.Repo != "o/r" || got.Number != 7 {
			t.Errorf("present columns must still be read: %+v", got)
		}
		if got.TokensUsed != 0 || !got.ReviewedAt.IsZero() {
			t.Errorf("absent columns keep their zero values: %+v", got)
		}
	})

	t.Run("every scanner reports drift, not just reviews", func(t *testing.T) {
		if _, err := scanCandidate(map[string]any{"number": "nope"}); err == nil {
			t.Error("scanCandidate swallowed an unreadable number")
		}
		if _, err := scanAuthor(map[string]any{"repo": "o/r"}); err != nil {
			t.Errorf("scanAuthor has no numeric columns, so it cannot drift here: %v", err)
		}
	})

	t.Run("the error names the column", func(t *testing.T) {
		_, err := scanReview(map[string]any{"tokens_used": "nope"})
		if err == nil || !strings.Contains(err.Error(), "tokens_used") {
			t.Errorf("the error must name the column so the drift is findable, got %v", err)
		}
	})
}

// TestRealVerdictMapping guards the Refreshed-detection invariant: exactly the engine's real-review
// decisions count as "reviewed at this SHA" (store.LastReview filters on
// this), while SKIPPED/ERROR outcomes stay re-surfaceable.
func TestRealVerdictMapping(t *testing.T) {
	cases := []struct {
		decision   string
		realReview bool
	}{
		{"APPROVED", true},
		{"COMMENTED", true},
		{"REQUESTED_CHANGES", true},
		{"SKIPPED", false},
		{"ERROR", false},
		{"", false},
		{"GARBAGE", false},
	}
	for _, tc := range cases {
		if got := IsRealVerdict(tc.decision); got != tc.realReview {
			t.Errorf("IsRealVerdict(%q) = %v, want %v", tc.decision, got, tc.realReview)
		}
	}
}
