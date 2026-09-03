package cli

import (
	"context"
	"os"
	"slices"
	"syscall"
	"testing"
	"time"
)

func TestShutdownController(t *testing.T) {
	t.Run("first signal stops intake but leaves reviewers draining", func(t *testing.T) {
		signals := make(chan os.Signal, 2)
		logs := &testLogs{}
		shutdown := newShutdownController(context.Background(), signals, logs.logf)
		defer shutdown.stop()

		signals <- syscall.SIGINT
		waitDone(t, shutdown.gracefulCtx(), "graceful context")
		assertNotDone(t, shutdown.reviewCtx(), "review context")
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
		waitDone(t, shutdown.gracefulCtx(), "graceful context")
		signals <- syscall.SIGINT
		waitDone(t, shutdown.reviewCtx(), "review context")
		if !logs.contains("again") || !logs.contains("force shutdown") {
			t.Fatalf("missing force shutdown log: %#v", logs.lines)
		}
	})

	// SIGTERM is the signal that actually arrives on a reboot or an upgrade;
	// launchd and systemd send it, and a human only ever sends SIGINT. Every
	// other case here uses SIGINT, so handling that alone would pass the whole
	// suite while leaving the unattended path -- the one where an in-flight
	// review is lost -- broken.
	t.Run("SIGTERM drains exactly like SIGINT", func(t *testing.T) {
		signals := make(chan os.Signal, 2)
		logs := &testLogs{}
		shutdown := newShutdownController(context.Background(), signals, logs.logf)
		defer shutdown.stop()

		signals <- syscall.SIGTERM
		waitDone(t, shutdown.gracefulCtx(), "graceful context")
		assertNotDone(t, shutdown.reviewCtx(), "review context")
		// The log has to name the signal: "why did the daemon stop" is the
		// first question after an unexplained restart.
		if !logs.contains("terminated") {
			t.Fatalf("shutdown log must name the signal that caused it: %#v", logs.lines)
		}
	})

	// A supervisor escalating SIGINT -> SIGTERM is the same "stop now" the
	// second Ctrl-C means. Matching on the signal rather than counting them
	// would leave in-flight reviews running past the deadline.
	t.Run("a different second signal still forces", func(t *testing.T) {
		signals := make(chan os.Signal, 2)
		logs := &testLogs{}
		shutdown := newShutdownController(context.Background(), signals, logs.logf)
		defer shutdown.stop()

		signals <- syscall.SIGINT
		waitDone(t, shutdown.gracefulCtx(), "graceful context")
		signals <- syscall.SIGTERM
		waitDone(t, shutdown.reviewCtx(), "review context")
		if !logs.contains("force shutdown") {
			t.Fatalf("missing force shutdown log: %#v", logs.lines)
		}
	})

	// The ordinary path: reviewers finish on their own and the daemon exits
	// through its own cleanup. Nothing was forced, so nothing may claim it
	// was -- a "force shutdown" line in the log after a clean drain would
	// send anyone reading it looking for a problem that did not happen.
	t.Run("a drain that completes on its own is not reported as forced", func(t *testing.T) {
		signals := make(chan os.Signal, 2)
		logs := &testLogs{}
		shutdown := newShutdownController(context.Background(), signals, logs.logf)

		signals <- syscall.SIGINT
		waitDone(t, shutdown.gracefulCtx(), "graceful context")

		shutdown.stop() // what serve's deferred cleanup does once draining is done
		waitDone(t, shutdown.reviewCtx(), "review context")
		if logs.contains("force shutdown") {
			t.Fatalf("a clean drain must not log a forced shutdown: %#v", logs.lines)
		}
	})

	t.Run("parent cancellation stops both contexts", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		shutdown := newShutdownController(parent, make(chan os.Signal), func(string, ...any) {})
		defer shutdown.stop()

		cancel()
		waitDone(t, shutdown.gracefulCtx(), "graceful context")
		waitDone(t, shutdown.reviewCtx(), "review context")
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

	// A forced shutdown still waits, briefly, for the reviewers to actually
	// die. Engines run in their own process group, so the terminal cannot kill
	// them and nothing else will reap them: returning before the kill lands
	// leaves them orphaned and still spending. The wait is bounded so a wedged
	// reviewer cannot block the exit it was asked to force.
	t.Run("force waits for the kill to land", func(t *testing.T) {
		forceCtx, force := context.WithCancel(context.Background())
		force()
		logs := &testLogs{}
		done := make(chan error, 1)

		finished := make(chan bool, 1)
		go func() { finished <- waitForScheduler(done, forceCtx, logs.logf) }()
		select {
		case <-finished:
			t.Fatal("force must not return before in-flight reviewers have exited")
		case <-time.After(50 * time.Millisecond):
		}

		close(done) // the reviewers died
		select {
		case forced := <-finished:
			if !forced {
				t.Fatal("canceled force context must force shutdown")
			}
		case <-time.After(time.Second):
			t.Fatal("force must return once the reviewers have exited")
		}
		if !logs.contains("killing in-flight reviewers") {
			t.Fatalf("missing force log: %#v", logs.lines)
		}
	})

	t.Run("force gives up on a wedged reviewer", func(t *testing.T) {
		defer func(d time.Duration) { forceDrain = d }(forceDrain)
		forceDrain = 20 * time.Millisecond

		forceCtx, force := context.WithCancel(context.Background())
		force()
		logs := &testLogs{}
		// Never closed: the reviewer will not exit.
		if !waitForScheduler(make(chan error), forceCtx, logs.logf) {
			t.Fatal("canceled force context must force shutdown")
		}
		if !logs.contains("did not exit within") {
			t.Fatalf("giving up must say so: %#v", logs.lines)
		}
	})
}

// The controller tests feed the signal channel directly, so they pass whether
// or not the daemon ever asked to be told about a signal. This pins the
// registration itself: drop SIGTERM and every unattended restart goes from
// draining in-flight reviews to killing them, with nothing else failing.
func TestShutdownSignalsCoverUnattendedRestarts(t *testing.T) {
	want := map[os.Signal]string{
		syscall.SIGINT:  "Ctrl-C",
		syscall.SIGTERM: "launchd/systemd/reboot",
	}
	for sig, who := range want {
		if !slices.Contains(shutdownSignals, sig) {
			t.Errorf("%v is not handled; %s sends it, and an unhandled signal kills in-flight reviews", sig, who)
		}
	}
}

// TestCostRatesAgreesWithLivePricing pins the one rule both valuation paths
// have to follow. A live review is priced by pricing.Rates.Cost; a backfilled
// row is priced by SQL that multiplies the flat CostRates fields. Cost applies
// a cache-write fallback (the input rate, when the price table lists no
// cache-write class) and the SQL cannot, so handing it the raw field valued
// those tokens at zero and silently under-priced every backfilled row.
