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
	go func() { done <- s.StartGraceful(ctx, context.Background()) }()
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
	go func() { done <- s.StartGraceful(stopCtx, reviewCtx) }()
	<-started
	<-started
	force()
	if err := <-done; err != context.Canceled {
		t.Errorf("StartGraceful error = %v, want context.Canceled", err)
	}
}

func TestStartGracefulWaitsWhenBothLoopsAreDisabled(t *testing.T) {
	stopCtx, stop := context.WithCancel(context.Background())
	defer stop()
	s := New(func() config.Config {
		return config.Config{
			Discovery: config.DiscoverySettings{Enabled: config.Bool(false)},
			Schedule:  config.ScheduleSettings{Enabled: config.Bool(false)},
		}
	}, nil, nil, "", nil, nil)
	s.reconcile = func(context.Context) error { return nil }
	s.loopRunner = func(context.Context, func() time.Duration, string, func(context.Context) error) {
		t.Fatal("disabled loop must not start")
	}

	done := make(chan error, 1)
	go func() { done <- s.StartGraceful(stopCtx, context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("returned before shutdown: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	stop()
	if err := <-done; err != context.Canceled {
		t.Errorf("StartGraceful error = %v, want context.Canceled", err)
	}
}
