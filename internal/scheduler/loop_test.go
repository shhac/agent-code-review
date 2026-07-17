package scheduler

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
)

func TestStartGracefulStartsConfiguredLoopsAndDrainsOnStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(func() config.Config { return config.Config{} }, nil, nil, "", nil, nil)
	s.reconcile = func(context.Context) error { return nil }
	started := make(chan string, 2)
	s.loopRunner = func(ctx context.Context, _ func() time.Duration, name string, _ func(context.Context) error) {
		started <- name
		<-ctx.Done()
	}

	done := make(chan error, 1)
	go func() { done <- s.StartGraceful(ctx, context.Background(), true, true) }()
	got := []string{<-started, <-started}
	sort.Strings(got)
	if want := []string{"discover", "review"}; got[0] != want[0] || got[1] != want[1] {
		t.Errorf("started loops = %v, want %v", got, want)
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Errorf("StartGraceful error = %v, want context.Canceled", err)
	}
}

func TestStartGracefulForceContextReturnsWithoutWaitingForLoops(t *testing.T) {
	stopCtx, stop := context.WithCancel(context.Background())
	defer stop()
	reviewCtx, force := context.WithCancel(context.Background())
	defer force()
	s := New(func() config.Config { return config.Config{} }, nil, nil, "", nil, nil)
	s.reconcile = func(context.Context) error { return nil }
	started := make(chan struct{}, 2)
	s.loopRunner = func(ctx context.Context, _ func() time.Duration, _ string, _ func(context.Context) error) {
		started <- struct{}{}
		<-ctx.Done()
	}

	done := make(chan error, 1)
	go func() { done <- s.StartGraceful(stopCtx, reviewCtx, true, true) }()
	<-started
	<-started
	force()
	if err := <-done; err != context.Canceled {
		t.Errorf("StartGraceful error = %v, want context.Canceled", err)
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
	s := New(func() config.Config { return config.Config{} }, nil, nil, "", nil, nil)
	s.heartbeat = time.Millisecond

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

	waitFor := func(cond func() bool, what string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(time.Millisecond)
		}
	}

	// Immediate first run, then nothing while the interval is an hour.
	waitFor(func() bool { return countRuns() == 1 }, "the immediate first run")
	time.Sleep(20 * time.Millisecond)
	if got := countRuns(); got != 1 {
		t.Fatalf("runs = %d before the interval elapsed, want 1", got)
	}

	// Shrinking the live interval makes the already-elapsed run due on the
	// next heartbeat: the documented config-reload contract.
	mu.Lock()
	interval = time.Millisecond
	mu.Unlock()
	waitFor(func() bool { return countRuns() >= 2 }, "the shrunk interval to trigger a run")

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
	s := New(func() config.Config {
		return config.Config{
			Discovery: config.DiscoverySettings{Enabled: config.Bool(true)},
			Schedule:  config.ScheduleSettings{Enabled: config.Bool(true)},
		}
	}, nil, nil, "", nil, nil)
	s.reconcile = func(context.Context) error { return nil }
	started := make(chan string, 2)
	s.loopRunner = func(ctx context.Context, _ func() time.Duration, name string, _ func(context.Context) error) {
		started <- name
		<-ctx.Done()
	}

	done := make(chan error, 1)
	go func() { done <- s.StartGraceful(ctx, context.Background(), false, true) }()
	if name := <-started; name != "review" {
		t.Errorf("started loop = %q, want review only", name)
	}
	select {
	case name := <-started:
		t.Errorf("unrequested loop %q must not start despite config enabling it", name)
	case <-time.After(10 * time.Millisecond):
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Errorf("StartGraceful error = %v, want context.Canceled", err)
	}
}
