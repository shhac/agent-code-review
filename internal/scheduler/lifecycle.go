// The scheduler's two entry points. StartGraceful is the daemon: reconcile,
// then the discovery loop and the review dispatcher until stopped. RunOnce is
// the `run` one-shot: reconcile, one sweep, drain, exit. They do the same
// three things in different orders, so they belong side by side.

package scheduler

import (
	"context"
	"sync"
	"time"
)

// StartGraceful runs the requested loops until gracefulCtx is cancelled:
// discovery receives gracefulCtx and is cancelled immediately, while
// in-flight reviewers receive reviewCtx and drain unless that second context
// is cancelled too. Both start immediately.
//
// Discovery is a periodic sweep and runs on the interval loop. Reviews are
// not: the dispatcher is one long-lived consumer of the queue, so it starts
// once and owns its own waiting (an idle poll when nothing is ready, a
// cooldown between hand-offs).
//
// The discovery/review switches are per-boot decisions owned by the caller
// (serve resolves config defaults + --no-* flags into them); nothing re-reads
// config's enabled flags mid-run, so a config edit cannot resurrect a loop
// this boot turned off. Callers with both switches off should not start the
// scheduler at all — called that way, this returns once reconciliation is
// done.
// gracefulCtx stops NEW work; reviewCtx ends work already running. Same two
// names serve.go uses, because the pair was called gracefulCtx/reviewCtx above
// this boundary and stopCtx/reviewCtx below it, in the one code path where
// confusing them kills a review mid-flight.
func (s *Scheduler) StartGraceful(gracefulCtx, reviewCtx context.Context, discovery, review bool) error {
	// A crashed daemon leaves a running run row (which would block cycles
	// for the whole lease window) and claimed queue rows (which would wait
	// it out too). Reconcile before the first tick so a restart resumes
	// immediately. Failure is logged, not fatal; the lease window is the
	// fallback that always works.
	if err := s.Reconcile(reviewCtx); err != nil {
		s.logf("reconcile: %v", err)
	}
	boot := s.cfg()
	var wg sync.WaitGroup
	if discovery {
		s.logf("scheduler: discovery every %s (config reloads live)", boot.DiscoverInterval())
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.loop(gracefulCtx, func() time.Duration { return s.cfg().DiscoverInterval() }, "discover", s.Discover)
		}()
	}
	if review {
		s.logf("scheduler: dispatching reviews, max parallel %d, %s between hand-offs, %s idle poll (config reloads live)",
			boot.MaxParallel(), boot.DispatchCooldown(), boot.Interval())
		wg.Add(1)
		go func() {
			defer wg.Done()
			// The daemon dispatcher only returns nil: a pull error is
			// surfaced through stopWhenIdle, which only `run` sets.
			_ = s.dispatch(gracefulCtx, reviewCtx, false)
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return gracefulCtx.Err()
	case <-reviewCtx.Done():
		return reviewCtx.Err()
	}
}

// RunOnce is the one-shot flow (`run`): reconcile leftovers, one discovery
// sweep, then drain whatever is dispatchable and exit.
func (s *Scheduler) RunOnce(ctx context.Context) error {
	if err := s.Reconcile(ctx); err != nil {
		s.logf("reconcile: %v", err)
	}
	if err := s.Discover(ctx); err != nil {
		return err
	}
	return s.dispatch(ctx, ctx, true)
}
