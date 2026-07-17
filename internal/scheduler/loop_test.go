package scheduler

import (
	"context"
	"sort"
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
