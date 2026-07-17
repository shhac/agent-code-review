package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
)

type testLogs struct {
	mu    sync.Mutex
	lines []string
}

func (l *testLogs) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *testLogs) contains(substr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Contains(strings.Join(l.lines, "\n"), substr)
}

func waitDone(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatalf("%s was not canceled", name)
	}
}

func assertNotDone(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	select {
	case <-ctx.Done():
		t.Fatalf("%s canceled too early", name)
	case <-time.After(25 * time.Millisecond):
	}
}

func TestShutdownController(t *testing.T) {
	t.Run("first signal stops intake but leaves reviewers draining", func(t *testing.T) {
		signals := make(chan os.Signal, 2)
		logs := &testLogs{}
		shutdown := newShutdownController(context.Background(), signals, logs.logf)
		defer shutdown.stop()

		signals <- syscall.SIGINT
		waitDone(t, shutdown.gracefulCtx, "graceful context")
		assertNotDone(t, shutdown.reviewCtx, "review context")
		if !logs.contains("stopping discovery and review scheduling") {
			t.Fatalf("missing graceful shutdown log: %#v", logs.lines)
		}
	})

	t.Run("second signal forces in-flight reviewers to stop", func(t *testing.T) {
		signals := make(chan os.Signal, 2)
		logs := &testLogs{}
		shutdown := newShutdownController(context.Background(), signals, logs.logf)
		defer shutdown.stop()

		signals <- syscall.SIGINT
		waitDone(t, shutdown.gracefulCtx, "graceful context")
		signals <- syscall.SIGINT
		waitDone(t, shutdown.reviewCtx, "review context")
		if !logs.contains("again") || !logs.contains("force shutdown") {
			t.Fatalf("missing force shutdown log: %#v", logs.lines)
		}
	})

	t.Run("parent cancellation stops both contexts", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		shutdown := newShutdownController(parent, make(chan os.Signal), func(string, ...any) {})
		defer shutdown.stop()

		cancel()
		waitDone(t, shutdown.gracefulCtx, "graceful context")
		waitDone(t, shutdown.reviewCtx, "review context")
	})
}

func TestWaitForScheduler(t *testing.T) {
	t.Run("completed scheduler exits gracefully", func(t *testing.T) {
		done := make(chan error)
		close(done)
		if waitForScheduler(done, context.Background(), func(string, ...any) {}) {
			t.Fatal("completed scheduler must not be treated as forced")
		}
	})

	t.Run("force context skips drain wait", func(t *testing.T) {
		forceCtx, force := context.WithCancel(context.Background())
		force()
		logs := &testLogs{}
		if !waitForScheduler(make(chan error), forceCtx, logs.logf) {
			t.Fatal("canceled force context must force shutdown")
		}
		if !logs.contains("force shutdown without waiting") {
			t.Fatalf("missing force-wait log: %#v", logs.lines)
		}
	})
}

func TestRunningLoopsPinsFlagsOverConfig(t *testing.T) {
	cfg := config.Config{}
	if got := runningLoops(serveOpts{}, cfg); !got.Discovery || !got.Review {
		t.Errorf("default loops = %+v, want both running", got)
	}
	if got := runningLoops(serveOpts{noReviews: true}, cfg); !got.Discovery || got.Review {
		t.Errorf("--no-reviews loops = %+v", got)
	}
	if got := runningLoops(serveOpts{noSchedule: true}, cfg); got.Discovery || got.Review {
		t.Errorf("--no-schedule loops = %+v", got)
	}
	cfg.Discovery.Enabled = config.Bool(false)
	cfg.Schedule.Enabled = config.Bool(false)
	if got := runningLoops(serveOpts{}, cfg); got.Discovery || got.Review {
		t.Errorf("disabled config loops = %+v", got)
	}
}

// TestStartDashboardBindConflict pins the "one daemon per address" guard:
// with the port already held, startDashboard must fail (naming the likely
// cause) BEFORE the scheduler could start — a second instance dies here,
// not after claiming a PR and spending an engine invocation.
func TestStartDashboardBindConflict(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	stopped := false
	_, err = startDashboard(ln.Addr().String(), nil, func(string, ...any) {}, func() { stopped = true })
	if err == nil {
		t.Fatal("binding an occupied address must fail")
	}
	if !strings.Contains(err.Error(), "another serve instance") {
		t.Errorf("error should hint at the double-daemon cause, got: %v", err)
	}
	if stopped {
		t.Error("the stop callback must not fire on a bind failure")
	}
}
