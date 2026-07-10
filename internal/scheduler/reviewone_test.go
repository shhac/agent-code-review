package scheduler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
	"github.com/shhac/agent-code-review/internal/usage"
)

// fakeSchedStore records the calls reviewOne makes; unused Store methods panic
// so an unexpected dependency shows up loudly. The mutex matters: processQueue
// fans reviewOne out across goroutines, so the recorders must be race-free.
type fakeSchedStore struct {
	store.Store // panic on anything not overridden

	mu        sync.Mutex
	allowed   bool
	claimLost bool // simulate losing the compare-and-swap to another worker
	claims    []store.Lease
	workDirs  []string
	completed []store.Review
}

func (f *fakeSchedStore) Claim(_ context.Context, _ string, _ int, l store.Lease) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimLost {
		return false, nil
	}
	f.claims = append(f.claims, l)
	f.workDirs = append(f.workDirs, l.WorkDir)
	return true, nil
}

func (f *fakeSchedStore) IsAuthorAllowed(context.Context, string, string) (bool, error) {
	return f.allowed, nil
}

func (f *fakeSchedStore) Complete(_ context.Context, r store.Review) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed = append(f.completed, r)
	return nil
}

// fakeEngine returns a fixed verdict and captures the prompt it was given
// (mutex-guarded: cycle tests run reviews concurrently).
type fakeEngine struct {
	verdict review.Verdict
	err     error

	mu     sync.Mutex
	prompt string
}

func (e *fakeEngine) Review(_ context.Context, req review.Request) (review.Verdict, error) {
	e.mu.Lock()
	e.prompt = req.Prompt
	e.mu.Unlock()
	return e.verdict, e.err
}
func (e *fakeEngine) Provenance(context.Context) review.Provenance {
	return review.Provenance{Engine: "fake"}
}

func newTestScheduler(fs *fakeSchedStore, fe *fakeEngine) *Scheduler {
	cfg := config.Config{Review: config.ReviewSettings{MainPrompt: "MAIN"}}
	s := New(func() config.Config { return cfg }, fs, nil, "the-gh-user", nil, nil)
	// Tests drive a fixed fake engine instead of the per-cycle rebuild.
	s.newEngine = func(config.Config) (review.Engine, error) { return fe, nil }
	// Default the candidacy recheck to "still a candidate" so tests exercise
	// the review path; precheck-specific tests override this.
	s.stillCandidate = func(context.Context, string, int) (bool, string, error) { return true, "", nil }
	return s
}

// reviewOne invokes Scheduler.reviewOne with the cycle inputs ReviewCycle
// would thread: the current config snapshot and the injected engine.
func reviewOne(s *Scheduler, fe *fakeEngine, c store.Candidate) error {
	return s.reviewOne(context.Background(), c, s.cfg(), fe)
}

// TestReviewOneCompletesEveryOutcome: every decision (real reviews, skips,
// and errors alike) ends as exactly one history row via Complete, carrying
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
			fe := &fakeEngine{verdict: review.Verdict{Decision: decision, Summary: "s", TokensUsed: 4242}}
			s := newTestScheduler(fs, fe)

			c := store.Candidate{Repo: "o/r", Number: 5, Author: "alice", HeadSHA: "sha1"}
			if err := reviewOne(s, fe, c); err != nil {
				t.Fatal(err)
			}
			if len(fs.claims) != 1 {
				t.Errorf("candidate must be claimed exactly once, got %d", len(fs.claims))
			}
			if len(fs.workDirs) != 1 || fs.workDirs[0] == "" {
				t.Errorf("claim must record the engine workdir, got %v", fs.workDirs)
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
			if r.TokensUsed != 4242 {
				t.Errorf("the engine's token count must reach history, got %d", r.TokensUsed)
			}
		})
	}
}

// TestReviewOneEngineErrorStillCompletes: a failed invocation propagates its
// error AND records an ERROR outcome; the queue row must not stay claimed
// forever (the old stuck-at-reviewing bug).
func TestReviewOneEngineErrorStillCompletes(t *testing.T) {
	fs := &fakeSchedStore{}
	fe := &fakeEngine{verdict: review.Verdict{Decision: review.DecisionError}, err: errors.New("boom")}
	s := newTestScheduler(fs, fe)

	err := reviewOne(s, fe, store.Candidate{Repo: "o/r", Number: 5, HeadSHA: "sha1"})
	if err == nil {
		t.Fatal("engine error must propagate")
	}
	if len(fs.completed) != 1 || fs.completed[0].Verdict != review.DecisionError {
		t.Errorf("failed invocation must record an ERROR outcome, got %+v", fs.completed)
	}
}

func TestReviewOneRecordsConfiguredCodexModelAndEffort(t *testing.T) {
	fs := &fakeSchedStore{}
	fe := &fakeEngine{verdict: review.Verdict{Decision: review.DecisionCommented}}
	s := newTestScheduler(fs, fe)
	if err := s.reviewOne(context.Background(), store.Candidate{Repo: "o/r", Number: 5, HeadSHA: "sha1"}, config.Config{}, &codexNamedEngine{fakeEngine: fe}); err != nil {
		t.Fatal(err)
	}
	if len(fs.completed) != 1 {
		t.Fatalf("completed = %d, want 1", len(fs.completed))
	}
	got := fs.completed[0]
	if got.Model != "gpt-5.6-terra" || got.Effort != "high" || got.CodexVersion != "Codex CLI 0.144.0" {
		t.Errorf("provenance = %+v", got)
	}
}

type codexNamedEngine struct{ *fakeEngine }

func (e *codexNamedEngine) Provenance(context.Context) review.Provenance {
	return review.Provenance{Engine: "codex", Model: "gpt-5.6-terra", Effort: "high", CodexVersion: "Codex CLI 0.144.0"}
}

// TestReviewOneClaimRace: losing the compare-and-swap claim to another
// worker (e.g. a second daemon instance sharing the store) must be a clean
// no-op: no engine spend, no outcome recorded, no error.
func TestReviewOneClaimRace(t *testing.T) {
	fs := &fakeSchedStore{claimLost: true}
	fe := &fakeEngine{verdict: review.Verdict{Decision: review.DecisionApproved}}
	s := newTestScheduler(fs, fe)

	if err := reviewOne(s, fe, store.Candidate{Repo: "o/r", Number: 6, HeadSHA: "sha1"}); err != nil {
		t.Fatalf("lost claim must not error, got %v", err)
	}
	if fe.prompt != "" {
		t.Error("engine must not run when the claim was lost")
	}
	if len(fs.completed) != 0 {
		t.Errorf("no outcome may be recorded for a lost claim, got %+v", fs.completed)
	}
}

// TestReviewOneClaimCarriesIdentity: the lease must record host+pid so boot
// reconciliation can tell this process's claims from a sibling's.
func TestReviewOneClaimCarriesIdentity(t *testing.T) {
	fs := &fakeSchedStore{}
	fe := &fakeEngine{verdict: review.Verdict{Decision: review.DecisionCommented}}
	s := newTestScheduler(fs, fe)
	if err := reviewOne(s, fe, store.Candidate{Repo: "o/r", Number: 6, HeadSHA: "sha1"}); err != nil {
		t.Fatal(err)
	}
	if len(fs.claims) != 1 || fs.claims[0].Host == "" || fs.claims[0].PID <= 0 || fs.claims[0].StaleAfter <= 0 {
		t.Errorf("claim lease must carry host/pid/staleness, got %+v", fs.claims)
	}
}

// TestReviewOneAllowedFlagReachesPrompt: the store's per-repo answer must flip
// the approval directive the engine sees.
func TestReviewOneAllowedFlagReachesPrompt(t *testing.T) {
	run := func(allowed bool) string {
		fs := &fakeSchedStore{allowed: allowed}
		fe := &fakeEngine{verdict: review.Verdict{Decision: review.DecisionCommented}}
		s := newTestScheduler(fs, fe)
		if err := reviewOne(s, fe, store.Candidate{Repo: "o/r", Number: 5, Author: "alice"}); err != nil {
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
	boundary := now.Add(-staleAfter) // exactly one window old, still leased
	stale := now.Add(-3 * time.Hour)
	queue := []store.Candidate{
		{Number: 1},                       // unclaimed
		{Number: 2, ClaimedAt: &fresh},    // in flight: leave alone
		{Number: 3, ClaimedAt: &stale},    // abandoned: reclaim
		{Number: 4, ClaimedAt: &boundary}, // boundary: still in flight
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
	s := New(func() config.Config { return cfg }, fs, nil, "u", nil, tripped)
	s.newEngine = func(config.Config) (review.Engine, error) { return fe, nil }
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
		if err := reviewOne(s, fe, c); err != nil {
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
		if err := reviewOne(s, fe, c); err != nil {
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
		if err := reviewOne(s, fe, c); err == nil {
			t.Fatal("recheck error must propagate")
		}
		if len(fs.completed) != 0 {
			t.Errorf("no outcome may be recorded on recheck error, got %+v", fs.completed)
		}
	})
}
