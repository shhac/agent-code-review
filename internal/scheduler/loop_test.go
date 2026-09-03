package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
)

func TestStartGracefulStartsConfiguredLoopsAndDrainsOnStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fs := &fakeDispatchStore{}
	swept := make(chan struct{}, 8)
	s := newLoopScheduler(fs, &fakeEngine{}, func(context.Context) ([]store.Candidate, error) {
		select {
		case swept <- struct{}{}:
		default:
		}
		return nil, nil
	})

	done := make(chan error, 1)
	go func() { done <- s.StartGraceful(Stop{Graceful: ctx, Force: context.Background()}, true, true) }()

	// Discovery really ran, so the discovery loop really started.
	select {
	case <-swept:
	case <-time.After(2 * time.Second):
		t.Fatal("the discovery loop never swept")
	}
	// The dispatcher really ran: it polled the queue it was given.
	waitFor(t, func() bool { return fs.pullCount() > 0 }, "the dispatcher to pull the queue")

	cancel()
	if err := <-done; err != context.Canceled {
		t.Errorf("StartGraceful error = %v, want context.Canceled", err)
	}
}

func TestStartGracefulForceContextReturnsWithoutWaitingForLoops(t *testing.T) {
	gracefulCtx, stop := context.WithCancel(context.Background())
	defer stop()
	reviewCtx, force := context.WithCancel(context.Background())
	defer force()

	fs := &fakeDispatchStore{}
	// A sweep that never returns stands in for a discovery loop still in
	// flight when the forced stop lands.
	held := make(chan struct{})
	defer close(held)
	s := newLoopScheduler(fs, &fakeEngine{}, func(ctx context.Context) ([]store.Candidate, error) {
		select {
		case <-held:
		case <-ctx.Done():
		}
		return nil, nil
	})

	done := make(chan error, 1)
	go func() { done <- s.StartGraceful(Stop{Graceful: gracefulCtx, Force: reviewCtx}, true, true) }()
	waitFor(t, func() bool { return fs.pullCount() > 0 }, "the loops to start")

	force()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("StartGraceful error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the forced context must return without waiting for the loops")
	}
}

// TestLoopCadence drives the real loop with a millisecond heartbeat and pins
// its contract: fn runs immediately on start, a shrunk live interval makes an
// already-elapsed run due on the next beat, and cancellation stops further
// runs. No test drove this production path before; only the pure `due` helper
// was covered.
func TestLoopCadence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newScheduler(Deps{
		Config:    func() config.Config { return config.Config{} },
		Heartbeat: time.Millisecond,
	})

	var mu sync.Mutex
	runs := 0
	interval := time.Hour // effectively "never due" until shrunk
	getInterval := func() time.Duration { mu.Lock(); defer mu.Unlock(); return interval }
	countRuns := func() int { mu.Lock(); defer mu.Unlock(); return runs }

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.loop(ctx, getInterval, "test", func(context.Context) error {
			mu.Lock()
			runs++
			mu.Unlock()
			return nil
		})
	}()

	// Immediate first run, then nothing while the interval is an hour.
	waitFor(t, func() bool { return countRuns() == 1 }, "the immediate first run")
	time.Sleep(20 * time.Millisecond)
	if got := countRuns(); got != 1 {
		t.Fatalf("runs = %d before the interval elapsed, want 1", got)
	}

	// Shrinking the live interval makes the already-elapsed run due on the
	// next heartbeat: the documented config-reload contract.
	mu.Lock()
	interval = time.Millisecond
	mu.Unlock()
	waitFor(t, func() bool { return countRuns() >= 2 }, "the shrunk interval to trigger a run")

	// Cancellation stops further runs.
	cancel()
	<-done
	final := countRuns()
	time.Sleep(20 * time.Millisecond)
	if got := countRuns(); got != final {
		t.Errorf("runs advanced after cancellation: %d -> %d", final, got)
	}
}

// TestStartGracefulSwitchesOwnTheLoops pins that the boot switches — not
// config's enabled flags — decide what runs: config says both loops are
// enabled, yet only the review loop is requested, so only it starts. A
// config edit can therefore never resurrect a loop this boot turned off.
func TestStartGracefulSwitchesOwnTheLoops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fs := &fakeDispatchStore{}
	swept := make(chan struct{}, 8)
	s := newScheduler(Deps{
		Store:     fs,
		Heartbeat: time.Millisecond,
		Sweeper: sweepFn(func(context.Context) ([]store.Candidate, error) {
			swept <- struct{}{}
			return nil, nil
		}),
		// Config enables BOTH loops; the boot flags must still win.
		Config: func() config.Config {
			return config.Config{
				Discovery: config.DiscoverySettings{Enabled: config.Bool(true)},
				Schedule:  config.ScheduleSettings{Enabled: config.Bool(true), Interval: "1ms", DispatchCooldown: "0s"},
				Review:    config.ReviewSettings{MainPrompt: "MAIN"},
			}
		},
	})

	done := make(chan error, 1)
	go func() { done <- s.StartGraceful(Stop{Graceful: ctx, Force: context.Background()}, false, true) }()

	waitFor(t, func() bool { return fs.pullCount() > 0 }, "the review dispatcher to start")
	select {
	case <-swept:
		t.Error("discovery must not run when its boot flag is off, despite config enabling it")
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	if err := <-done; err != context.Canceled {
		t.Errorf("StartGraceful error = %v, want context.Canceled", err)
	}
}

// TestStartGracefulWiresBothContextsThroughToTheEngine drives StartGraceful
// end to end and pins the contract serve prints on the first signal:
// cancelling Stop.Graceful alone stops NEW work while the in-flight review
// runs to completion, and only Stop.Force ends the running one.
//
// Stop now makes swapping the two impossible to compile, but the wiring is
// still worth an end-to-end test: nothing else proves the context the engine
// receives is the one that survives a graceful stop.
func TestStartGracefulWiresBothContextsThroughToTheEngine(t *testing.T) {
	fs := &fakeDispatchStore{queue: []store.Candidate{
		{Repo: "o/r", Number: 1, HeadSHA: "s1"},
		{Repo: "o/r", Number: 2, HeadSHA: "s2"},
	}}
	engineCtx := make(chan context.Context, 4)
	started := make(chan int, 4)
	release := make(chan struct{})
	fe := &fakeEngine{seen: engineCtx, started: started, release: release, verdict: review.Verdict{Decision: review.DecisionCommented}}

	s := newScheduler(Deps{
		Store:  fs,
		GHUser: "u",
		Config: func() config.Config {
			return config.Config{
				Review:   config.ReviewSettings{MainPrompt: "MAIN"},
				Schedule: config.ScheduleSettings{MaxParallel: 1, Interval: "1ms", DispatchCooldown: "0s"},
			}
		},
		NewEngine: fixedEngine(fe),
		Heartbeat: time.Millisecond,
	})

	gracefulCtx, stop := context.WithCancel(context.Background())
	reviewCtx, force := context.WithCancel(context.Background())
	defer force()

	done := make(chan error, 1)
	go func() { done <- s.StartGraceful(Stop{Graceful: gracefulCtx, Force: reviewCtx}, false, true) }()

	// Wait for the first reviewer to be in flight, then request the graceful
	// stop while it is still running.
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("no reviewer started")
	}
	stop()

	// The in-flight engine must NOT have been handed a context that the
	// graceful stop cancels: that is the whole bargain.
	var got context.Context
	select {
	case got = <-engineCtx:
	case <-time.After(5 * time.Second):
		t.Fatal("engine context never captured")
	}
	if got.Err() != nil {
		t.Fatalf("the engine's context was cancelled by the graceful stop (%v); "+
			"the in-flight review would die instead of finishing", got.Err())
	}

	// And no SECOND reviewer starts once the stop has landed.
	select {
	case <-started:
		t.Error("a graceful stop must not launch another reviewer")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StartGraceful did not return after draining")
	}

	// Now prove the other half: reviewCtx is what actually reaches the engine,
	// so a forced shutdown does end a running review.
	if got.Err() != nil {
		t.Fatal("precondition")
	}
	force()
	if got.Err() == nil {
		t.Error("the engine's context must be cancelled by the forced stop")
	}
}

// waitFor polls until cond holds, failing the test on timeout. Polling rather
// than sleeping keeps the lifecycle tests honest without making them
// timing-sensitive.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// newLoopScheduler builds a scheduler whose real loops run without real
// waits, so StartGraceful's orchestration is exercised rather than replaced by
// stubs.
func newLoopScheduler(fs *fakeDispatchStore, fe review.Engine, sweep sweepFn) *Scheduler {
	return newScheduler(Deps{
		Store:     fs,
		NewEngine: fixedEngine(fe),
		Sweeper:   sweep,
		Heartbeat: time.Millisecond,
	})
}

// TestDue pins the heartbeat's firing rule, including the live-reload
// property: a cadence shrunk below the already-elapsed time makes the run
// due on the next beat, without waiting out the old interval.
func TestDue(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		elapsed  time.Duration
		interval time.Duration
		want     bool
	}{
		{"just under the interval", 14 * time.Minute, 15 * time.Minute, false},
		{"exactly at the interval", 15 * time.Minute, 15 * time.Minute, true},
		{"past the interval", 16 * time.Minute, 15 * time.Minute, true},
		{"interval shrunk below elapsed", 20 * time.Minute, 15 * time.Minute, true},
		{"interval grown above elapsed", 20 * time.Minute, 90 * time.Minute, false},
	}
	for _, tc := range cases {
		if got := due(now.Add(-tc.elapsed), now, tc.interval); got != tc.want {
			t.Errorf("%s: due = %v, want %v", tc.name, got, tc.want)
		}
	}
}
