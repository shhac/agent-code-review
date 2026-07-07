package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
	"github.com/shhac/agent-code-review/internal/usage"
)

// fakeSchedStore records the calls reviewOne makes; unused Store methods panic
// so an unexpected dependency shows up loudly.
type fakeSchedStore struct {
	store.Store // panic on anything not overridden

	allowed   bool
	claims    []time.Time
	completed []store.Review
}

func (f *fakeSchedStore) Claim(_ context.Context, _ string, _ int, at time.Time) error {
	f.claims = append(f.claims, at)
	return nil
}

func (f *fakeSchedStore) IsAuthorAllowed(context.Context, string, string) (bool, error) {
	return f.allowed, nil
}

func (f *fakeSchedStore) Complete(_ context.Context, r store.Review) error {
	f.completed = append(f.completed, r)
	return nil
}

// fakeEngine returns a fixed verdict and captures the prompt it was given.
type fakeEngine struct {
	verdict review.Verdict
	err     error
	prompt  string
}

func (e *fakeEngine) Name() string { return "fake" }
func (e *fakeEngine) Review(_ context.Context, req review.Request) (review.Verdict, error) {
	e.prompt = req.Prompt
	return e.verdict, e.err
}

func newTestScheduler(fs *fakeSchedStore, fe *fakeEngine) *Scheduler {
	cfg := config.Config{Review: config.ReviewSettings{MainPrompt: "MAIN"}}
	s := New(cfg, fs, nil, fe, "the-gh-user", nil, nil)
	// Default the candidacy recheck to "still a candidate" so tests exercise
	// the review path; precheck-specific tests override this.
	s.stillCandidate = func(context.Context, string, int) (bool, string, error) { return true, "", nil }
	return s
}

// TestReviewOneCompletesEveryOutcome: every decision — real reviews, skips,
// and errors alike — ends as exactly one history row via Complete, carrying
// the reviewed SHA (Complete's delete is gated on it).
func TestReviewOneCompletesEveryOutcome(t *testing.T) {
	decisions := []string{
		review.DecisionApproved,
		review.DecisionCommented,
		review.DecisionRequestedChanges,
		review.DecisionSkipped,
		review.DecisionError,
	}
	for _, decision := range decisions {
		t.Run(decision, func(t *testing.T) {
			fs := &fakeSchedStore{}
			fe := &fakeEngine{verdict: review.Verdict{Decision: decision, Summary: "s"}}
			s := newTestScheduler(fs, fe)

			c := store.Candidate{Repo: "o/r", Number: 5, Author: "alice", HeadSHA: "sha1"}
			if err := s.reviewOne(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			if len(fs.claims) != 1 {
				t.Errorf("candidate must be claimed exactly once, got %d", len(fs.claims))
			}
			if len(fs.completed) != 1 {
				t.Fatalf("every outcome must Complete exactly once, got %d", len(fs.completed))
			}
			r := fs.completed[0]
			if r.Verdict != decision {
				t.Errorf("verdict = %q, want %q", r.Verdict, decision)
			}
			if r.HeadSHA != "sha1" {
				t.Errorf("history must carry the reviewed SHA, got %q", r.HeadSHA)
			}
		})
	}
}

// TestReviewOneEngineErrorStillCompletes: a failed invocation propagates its
// error AND records an ERROR outcome — the queue row must not stay claimed
// forever (the old stuck-at-reviewing bug).
func TestReviewOneEngineErrorStillCompletes(t *testing.T) {
	fs := &fakeSchedStore{}
	fe := &fakeEngine{verdict: review.Verdict{Decision: review.DecisionError}, err: errors.New("boom")}
	s := newTestScheduler(fs, fe)

	err := s.reviewOne(context.Background(), store.Candidate{Repo: "o/r", Number: 5, HeadSHA: "sha1"})
	if err == nil {
		t.Fatal("engine error must propagate")
	}
	if len(fs.completed) != 1 || fs.completed[0].Verdict != review.DecisionError {
		t.Errorf("failed invocation must record an ERROR outcome, got %+v", fs.completed)
	}
}

// TestReviewOneAllowedFlagReachesPrompt: the store's per-repo answer must flip
// the approval directive the engine sees.
func TestReviewOneAllowedFlagReachesPrompt(t *testing.T) {
	run := func(allowed bool) string {
		fs := &fakeSchedStore{allowed: allowed}
		fe := &fakeEngine{verdict: review.Verdict{Decision: review.DecisionCommented}}
		s := newTestScheduler(fs, fe)
		if err := s.reviewOne(context.Background(), store.Candidate{Repo: "o/r", Number: 5, Author: "alice"}); err != nil {
			t.Fatal(err)
		}
		return fe.prompt
	}
	if p := run(true); !strings.Contains(p, "MAY approve") {
		t.Errorf("allowed author must yield MAY-approve directive, got:\n%.200s", p)
	}
	if p := run(false); !strings.Contains(p, "DO NOT approve") {
		t.Errorf("disallowed author must yield DO-NOT-approve directive, got:\n%.200s", p)
	}
}

// TestAvailableCandidates pins the lease semantics: unclaimed rows and stale
// claims are workable; a fresh claim is another worker's in-flight review.
func TestAvailableCandidates(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	staleAfter := 2 * time.Hour
	fresh := now.Add(-time.Hour)
	boundary := now.Add(-staleAfter) // exactly one window old — still leased
	stale := now.Add(-3 * time.Hour)
	queue := []store.Candidate{
		{Number: 1},                       // unclaimed
		{Number: 2, ClaimedAt: &fresh},    // in flight — leave alone
		{Number: 3, ClaimedAt: &stale},    // abandoned — reclaim
		{Number: 4, ClaimedAt: &boundary}, // boundary — still in flight
	}
	got := availableCandidates(queue, now, staleAfter)
	if len(got) != 2 || got[0].Number != 1 || got[1].Number != 3 {
		t.Fatalf("availableCandidates = %+v, want candidates 1 and 3", got)
	}
}

// TestReviewCyclePausedByUsageFloor: a tripped floor returns before ANY store
// access (the embedded nil Store panics on use, so reaching the store fails
// the test) and records no run.
func TestReviewCyclePausedByUsageFloor(t *testing.T) {
	fs := &fakeSchedStore{}
	fe := &fakeEngine{}
	cfg := config.Config{}
	tripped := func() usage.Snapshot {
		return usage.Snapshot{
			FetchedAt: time.Now(),
			Primary:   &usage.Window{UsedPercent: 95, WindowMins: 300},
		}
	}
	s := New(cfg, fs, nil, fe, "u", nil, tripped)
	if err := s.ReviewCycle(context.Background()); err != nil {
		t.Fatalf("paused cycle must return nil, got %v", err)
	}
}

// TestReviewOnePrecheck pins the pre-review revalidation: stale discovered
// candidates are skipped without touching the engine; manual adds bypass the
// check entirely; a recheck error propagates without recording an outcome
// (the stale lease retries it next cycle).
func TestReviewOnePrecheck(t *testing.T) {
	t.Run("stale discovered candidate records a precheck skip", func(t *testing.T) {
		fs := &fakeSchedStore{}
		fe := &fakeEngine{verdict: review.Verdict{Decision: review.DecisionApproved}}
		s := newTestScheduler(fs, fe)
		s.stillCandidate = func(context.Context, string, int) (bool, string, error) {
			return false, "already approved", nil
		}
		c := store.Candidate{Repo: "o/r", Number: 7, HeadSHA: "sha1", Source: store.SourceDiscovered}
		if err := s.reviewOne(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		if fe.prompt != "" {
			t.Error("engine must not run for a stale candidate")
		}
		if len(fs.completed) != 1 || fs.completed[0].Verdict != review.DecisionSkipped || fs.completed[0].Engine != store.EnginePrecheck {
			t.Errorf("stale candidate must complete as a precheck SKIPPED, got %+v", fs.completed)
		}
	})

	t.Run("manual candidates bypass the recheck", func(t *testing.T) {
		fs := &fakeSchedStore{}
		fe := &fakeEngine{verdict: review.Verdict{Decision: review.DecisionCommented}}
		s := newTestScheduler(fs, fe)
		s.stillCandidate = func(context.Context, string, int) (bool, string, error) {
			t.Error("manual candidate must not be rechecked")
			return false, "", nil
		}
		c := store.Candidate{Repo: "o/r", Number: 8, HeadSHA: "sha1", Source: store.SourceManual}
		if err := s.reviewOne(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		if len(fs.completed) != 1 || fs.completed[0].Verdict != review.DecisionCommented {
			t.Errorf("manual candidate must be reviewed normally, got %+v", fs.completed)
		}
	})

	t.Run("recheck error propagates and records nothing", func(t *testing.T) {
		fs := &fakeSchedStore{}
		fe := &fakeEngine{}
		s := newTestScheduler(fs, fe)
		s.stillCandidate = func(context.Context, string, int) (bool, string, error) {
			return false, "", errors.New("gh unavailable")
		}
		c := store.Candidate{Repo: "o/r", Number: 9, HeadSHA: "sha1", Source: store.SourceDiscovered}
		if err := s.reviewOne(context.Background(), c); err == nil {
			t.Fatal("recheck error must propagate")
		}
		if len(fs.completed) != 0 {
			t.Errorf("no outcome may be recorded on recheck error, got %+v", fs.completed)
		}
	})
}
