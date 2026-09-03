package cli

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/review"
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

// The daemon meters every wired engine, so this list must be derived from the
// engine roster rather than restated. A hand-written list would silently skip
// a third engine: no compile error, no failing test, just no usage polled.
func TestUsageSourcesCoversEveryWiredEngine(t *testing.T) {
	cfg := config.Config{Review: config.ReviewSettings{
		Codex:  config.CodexSettings{Bin: "codex-dev"},
		Claude: config.ClaudeSettings{Bin: "claude-dev"},
	}}
	got := usageSources(cfg)
	if len(got) != len(review.Engines) {
		t.Fatalf("got %d sources, want one per wired engine (%d)", len(got), len(review.Engines))
	}
	bins := map[string]string{}
	for _, src := range got {
		bins[src.Engine] = src.Bin
	}
	for _, engine := range review.Engines {
		if _, ok := bins[engine]; !ok {
			t.Errorf("engine %q is wired but never metered", engine)
		}
	}
	if bins["codex"] != "codex-dev" || bins["claude"] != "claude-dev" {
		t.Errorf("bins = %v, want each engine's configured binary", bins)
	}
}
