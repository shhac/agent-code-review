package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
)

// fakeCycleStore extends the reviewOne fake with the run-lock and queue
// surface ReviewCycle drives; unused Store methods still panic loudly via
// the embedded nil interface.
type fakeCycleStore struct {
	fakeSchedStore

	queue     []store.Candidate
	queueErr  error
	activeRun bool
	started   []store.Run
	finished  []string // statuses passed to FinishRun
}

func (f *fakeCycleStore) ActiveRun(context.Context, time.Duration) (store.Run, bool, error) {
	return store.Run{}, f.activeRun, nil
}

func (f *fakeCycleStore) StartRun(_ context.Context, r store.Run) error {
	f.started = append(f.started, r)
	return nil
}

func (f *fakeCycleStore) FinishRun(_ context.Context, _ string, status string) error {
	f.finished = append(f.finished, status)
	return nil
}

func (f *fakeCycleStore) ListQueue(context.Context, string) ([]store.Candidate, error) {
	return f.queue, f.queueErr
}

func newCycleScheduler(fs *fakeCycleStore, fe *fakeEngine) *Scheduler {
	cfg := config.Config{Review: config.ReviewSettings{MainPrompt: "MAIN"}}
	s := New(func() config.Config { return cfg }, fs, nil, "the-gh-user", nil, nil)
	s.newEngine = func(config.Config) (review.Engine, error) { return fe, nil }
	s.stillCandidate = func(context.Context, string, int) (bool, string, error) { return true, "", nil }
	return s
}

type blockingEngine struct {
	started chan int
	release chan struct{}
	once    sync.Once
}

func (e *blockingEngine) Provenance(context.Context) review.Provenance {
	return review.Provenance{Engine: "blocking"}
}

func (e *blockingEngine) Review(ctx context.Context, req review.Request) (review.Verdict, error) {
	e.started <- req.Candidate.Number
	e.once.Do(func() {
		<-e.release
	})
	select {
	case <-ctx.Done():
		return review.Verdict{Decision: review.DecisionError}, ctx.Err()
	default:
		return review.Verdict{Decision: review.DecisionCommented}, nil
	}
}

// TestProcessQueueGracefulStop pins serve's first-Ctrl-C behavior: stop
// launching new reviewers, but let already-started reviewers finish.
func TestProcessQueueGracefulStop(t *testing.T) {
	fs := &fakeCycleStore{}
	fe := &blockingEngine{started: make(chan int, 2), release: make(chan struct{})}
	s := New(func() config.Config {
		return config.Config{Review: config.ReviewSettings{MainPrompt: "MAIN"}}
	}, fs, nil, "the-gh-user", nil, nil)
	s.stillCandidate = func(context.Context, string, int) (bool, string, error) { return true, "", nil }
	stopCtx, stop := context.WithCancel(context.Background())
	defer stop()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.processQueue(stopCtx, context.Background(), []store.Candidate{
			{Repo: "o/r", Number: 1, HeadSHA: "s1"},
			{Repo: "o/r", Number: 2, HeadSHA: "s2"},
		}, config.Config{Schedule: config.ScheduleSettings{MaxParallel: 1}, Review: config.ReviewSettings{MainPrompt: "MAIN"}}, fe)
	}()

	if got := <-fe.started; got != 1 {
		t.Fatalf("first reviewer = #%d, want #1", got)
	}
	stop()
	select {
	case got := <-fe.started:
		t.Fatalf("graceful stop must not launch a second reviewer, launched #%d", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(fe.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("processQueue did not drain the in-flight reviewer")
	}
	if len(fs.completed) != 1 || fs.completed[0].Number != 1 {
		t.Errorf("only the in-flight reviewer should complete, got %+v", fs.completed)
	}
}

// TestReviewCycle pins the cycle orchestration: take the run-lock, review
// every available candidate, record each outcome, release the lock as done.
func TestReviewCycle(t *testing.T) {
	t.Run("reviews every available candidate", func(t *testing.T) {
		fresh := time.Now().Add(-time.Minute)
		fs := &fakeCycleStore{queue: []store.Candidate{
			{Repo: "o/r", Number: 1, HeadSHA: "s1"},
			{Repo: "o/r", Number: 2, HeadSHA: "s2"},
			{Repo: "o/r", Number: 3, HeadSHA: "s3", ClaimedAt: &fresh}, // in flight elsewhere
		}}
		fe := &fakeEngine{verdict: review.Verdict{Decision: review.DecisionCommented}}
		s := newCycleScheduler(fs, fe)

		if err := s.ReviewCycle(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(fs.started) != 1 {
			t.Fatalf("exactly one run must be recorded, got %d", len(fs.started))
		}
		if len(fs.finished) != 1 || fs.finished[0] != "done" {
			t.Errorf("run must finish as done, got %v", fs.finished)
		}
		if len(fs.completed) != 2 {
			t.Errorf("both unclaimed candidates must complete, got %d", len(fs.completed))
		}
		for _, r := range fs.completed {
			if r.Number == 3 {
				t.Error("a freshly claimed candidate must not be re-reviewed")
			}
		}
	})

	t.Run("active run skips the cycle", func(t *testing.T) {
		fs := &fakeCycleStore{activeRun: true, queue: []store.Candidate{{Repo: "o/r", Number: 1}}}
		s := newCycleScheduler(fs, &fakeEngine{})
		if err := s.ReviewCycle(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(fs.started) != 0 || len(fs.completed) != 0 {
			t.Errorf("run-lock must prevent any work, got started=%d completed=%d", len(fs.started), len(fs.completed))
		}
	})

	t.Run("queue error propagates without recording a run", func(t *testing.T) {
		fs := &fakeCycleStore{queueErr: errors.New("db gone")}
		s := newCycleScheduler(fs, &fakeEngine{})
		if err := s.ReviewCycle(context.Background()); err == nil {
			t.Fatal("queue error must propagate")
		}
		if len(fs.started) != 0 || len(fs.finished) != 0 {
			t.Errorf("queue error happens before the run-lock, got started=%d finished=%v", len(fs.started), fs.finished)
		}
	})

	t.Run("engine build error aborts before the run-lock", func(t *testing.T) {
		fs := &fakeCycleStore{queue: []store.Candidate{{Repo: "o/r", Number: 1}}}
		s := newCycleScheduler(fs, &fakeEngine{})
		s.newEngine = func(config.Config) (review.Engine, error) { return nil, errors.New("bad engine") }
		if err := s.ReviewCycle(context.Background()); err == nil {
			t.Fatal("engine build error must propagate")
		}
		if len(fs.started) != 0 {
			t.Error("no run may be recorded when the engine can't be built")
		}
	})

	t.Run("empty queue is an idle no-op recording nothing", func(t *testing.T) {
		fs := &fakeCycleStore{}
		s := newCycleScheduler(fs, &fakeEngine{})
		if err := s.ReviewCycle(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(fs.started) != 0 || len(fs.finished) != 0 {
			t.Errorf("idle cycle must record no run (1m cadence would flood the runs table), got started=%d finished=%v", len(fs.started), fs.finished)
		}
	})

	t.Run("held candidates are skipped; an all-held queue is idle", func(t *testing.T) {
		soon := time.Now().Add(30 * time.Minute)
		fs := &fakeCycleStore{queue: []store.Candidate{
			{Repo: "o/r", Number: 1, HeadSHA: "s1", EligibleAt: &soon, HoldReason: store.HoldCooldown},
			{Repo: "o/r", Number: 2, HeadSHA: "s2"},
		}}
		fe := &fakeEngine{verdict: review.Verdict{Decision: review.DecisionCommented}}
		s := newCycleScheduler(fs, fe)
		if err := s.ReviewCycle(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(fs.completed) != 1 || fs.completed[0].Number != 2 {
			t.Errorf("only the eligible candidate may be reviewed, got %+v", fs.completed)
		}

		// Every row held → idle cycle, nothing recorded.
		fs = &fakeCycleStore{queue: []store.Candidate{
			{Repo: "o/r", Number: 1, HeadSHA: "s1", EligibleAt: &soon, HoldReason: store.HoldSettling},
		}}
		s = newCycleScheduler(fs, fe)
		if err := s.ReviewCycle(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(fs.started) != 0 || len(fs.completed) != 0 {
			t.Errorf("all-held queue must be an idle cycle, got started=%d completed=%d", len(fs.started), len(fs.completed))
		}

		// An expired hold is eligible again.
		past := time.Now().Add(-time.Minute)
		fs = &fakeCycleStore{queue: []store.Candidate{
			{Repo: "o/r", Number: 3, HeadSHA: "s3", EligibleAt: &past, HoldReason: store.HoldCooldown},
		}}
		s = newCycleScheduler(fs, fe)
		if err := s.ReviewCycle(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(fs.completed) != 1 || fs.completed[0].Number != 3 {
			t.Errorf("expired hold must be reviewable, got %+v", fs.completed)
		}
	})
}

// TestTail pins the log-tail formatter: whitespace-trimmed, newline-flattened,
// last-n-bytes with an ellipsis when truncated.
func TestTail(t *testing.T) {
	if got := tail("  short\nlines  ", 100); got != "short | lines" {
		t.Errorf("tail = %q", got)
	}
	if got := tail("aaaaabbbbb", 5); got != "…bbbbb" {
		t.Errorf("truncated tail = %q", got)
	}
}
