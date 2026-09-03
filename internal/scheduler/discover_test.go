package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/shhac/agent-code-review/internal/store"
)

// TestDiscoverSkipsWhileASweepIsInFlight pins the in-flight guard. It exists
// so gh sweeps cannot pile up when one outlives the discovery interval —
// a condition that otherwise only appears in production, and which nothing
// could reach while the discoverer was a concrete *discover.Discoverer.
func TestDiscoverSkipsWhileASweepIsInFlight(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	calls := make(chan struct{}, 8)
	var once sync.Once

	s := newDispatchScheduler(&fakeDispatchStore{}, &fakeEngine{})
	s.sweeper = sweepFn(func(context.Context) ([]store.Candidate, error) {
		calls <- struct{}{}
		once.Do(func() { close(entered); <-release })
		return nil, nil
	})

	first := make(chan error, 1)
	go func() { first <- s.Discover(context.Background()) }()
	<-entered

	// A second tick arriving mid-sweep must return immediately without
	// invoking the sweeper again.
	if err := s.Discover(context.Background()); err != nil {
		t.Fatalf("a skipped sweep is not an error, got %v", err)
	}
	if len(calls) != 1 {
		t.Errorf("the sweeper ran %d times, want 1: the second tick must be skipped", len(calls))
	}

	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}

	// The guard is released once the first sweep returns.
	if err := s.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Errorf("the sweeper ran %d times, want 2: the guard must release", len(calls))
	}
}

// TestDiscoverReleasesTheGuardOnError: a sweep that fails must not wedge
// discovery for the life of the daemon.
func TestDiscoverReleasesTheGuardOnError(t *testing.T) {
	calls := 0
	s := newDispatchScheduler(&fakeDispatchStore{}, &fakeEngine{})
	s.sweeper = sweepFn(func(context.Context) ([]store.Candidate, error) {
		calls++
		return nil, errors.New("gh exploded")
	})

	if err := s.Discover(context.Background()); err == nil {
		t.Fatal("a sweep error must propagate")
	}
	if err := s.Discover(context.Background()); err == nil {
		t.Fatal("a sweep error must propagate")
	}
	if calls != 2 {
		t.Errorf("the sweeper ran %d times, want 2: a failed sweep must release the guard", calls)
	}
}

// TestRunOnceErrorContract pins what `run` promises a cron entry: a
// reconcile failure is logged and tolerated (the lease window is the fallback
// that always works), while a discovery failure is fatal so the run exits
// non-zero rather than silently reviewing a stale queue.
func TestRunOnceErrorContract(t *testing.T) {
	t.Run("a discovery failure aborts the run", func(t *testing.T) {
		fs := &fakeDispatchStore{queue: []store.Candidate{{Repo: "o/r", Number: 1, HeadSHA: "s1"}}}
		s := newDispatchScheduler(fs, &fakeEngine{})
		s.sweeper = sweepFn(func(context.Context) ([]store.Candidate, error) {
			return nil, errors.New("gh unavailable")
		})
		if err := s.RunOnce(context.Background()); err == nil {
			t.Fatal("a discovery failure must propagate so cron exits non-zero")
		}
		if len(fs.completed) != 0 {
			t.Errorf("nothing may be reviewed after a failed sweep, got %+v", fs.completed)
		}
	})

	t.Run("a reconcile failure is tolerated", func(t *testing.T) {
		fs := &fakeDispatchStore{
			queue:    []store.Candidate{{Repo: "o/r", Number: 1, HeadSHA: "s1"}},
			queueErr: errors.New("transient"),
		}
		// Reconcile lists the queue, so the seeded error fails it. The run
		// must carry on to discovery rather than aborting there.
		s := newDispatchScheduler(fs, &fakeEngine{})
		swept := false
		s.sweeper = sweepFn(func(context.Context) ([]store.Candidate, error) {
			swept = true
			return nil, nil
		})
		_ = s.RunOnce(context.Background())
		if !swept {
			t.Error("a reconcile failure must not stop the run reaching discovery")
		}
	})
}
