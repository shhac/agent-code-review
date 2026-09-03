// The daemon's two-stage stop. The first signal cancels only new work and
// lets in-flight reviews finish; the second ends them. Both contexts are
// threaded from here, and confusing them kills a review mid-flight.

package cli

import (
	"context"
	"os"
	"syscall"
	"time"

	"github.com/shhac/agent-code-review/internal/scheduler"
)

type shutdownController struct {
	// stopCtxs is the pair the scheduler takes whole, so the two contexts
	// cannot be handed over the wrong way round.
	stopCtxs scheduler.Stop
	graceful func()
	stop     func()
}

// gracefulCtx stops NEW work; reviewCtx ends work already running.
func (c shutdownController) gracefulCtx() context.Context { return c.stopCtxs.Graceful }
func (c shutdownController) reviewCtx() context.Context   { return c.stopCtxs.Force }

func newShutdownController(ctx context.Context, signals <-chan os.Signal, logf scheduler.Logf) shutdownController {
	gracefulCtx, gracefulStop := context.WithCancel(ctx)
	reviewCtx, forceStop := context.WithCancel(ctx)
	go func() {
		select {
		case <-ctx.Done():
			gracefulStop()
			forceStop()
		case sig := <-signals:
			logf("shutdown: received %s: stopping discovery and review scheduling; waiting for in-flight reviewers. Press Ctrl-C again to force exit.", sig)
			gracefulStop()
			select {
			case <-ctx.Done():
				forceStop()
			case sig := <-signals:
				logf("shutdown: received %s again: force shutdown", sig)
				forceStop()
			case <-reviewCtx.Done():
			}
		}
	}()
	return shutdownController{
		stopCtxs: scheduler.Stop{Graceful: gracefulCtx, Force: reviewCtx},
		graceful: gracefulStop,
		stop: func() {
			gracefulStop()
			forceStop()
		},
	}
}

// shutdownSignals is what the daemon stops on. SIGTERM matters as much as
// SIGINT and is easier to forget: it is what launchd, systemd and a reboot
// send, so dropping it would leave every unattended restart killing in-flight
// reviews outright instead of draining them. Named so a test can pin the set,
// which a test feeding the signal channel directly cannot.
var shutdownSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM}

// forceDrain bounds how long a forced shutdown waits for in-flight reviewers
// to actually die after their contexts are cancelled. A var so tests can
// shrink it, like the scheduler's heartbeat.
var forceDrain = 5 * time.Second

func waitForScheduler(done <-chan error, forceCtx context.Context, logf scheduler.Logf) bool {
	if done == nil {
		return false
	}
	select {
	case <-done:
		return false
	case <-forceCtx.Done():
		logf("shutdown: force shutdown, killing in-flight reviewers")
		// Returning straight away would exit before the kill lands. Engines
		// run in their own process group now (so the terminal cannot kill a
		// review the first Ctrl-C promised to wait for), which means nothing
		// else will reap them: they would outlive the daemon and keep
		// spending against the subscription with nobody reading their output.
		// Cancellation SIGKILLs the group, so in practice this waits
		// milliseconds; the bound only stops a wedged reviewer from blocking
		// the exit it was asked to force.
		select {
		case <-done:
		case <-time.After(forceDrain):
			logf("shutdown: in-flight reviewers did not exit within %s; exiting anyway", forceDrain)
		}
		return true
	}
}
