package scheduler

import (
	"context"
	"sync"
	"time"
)

// Start runs the enabled loops until ctx is cancelled. Cancellation is forceful:
// in-flight reviewers receive ctx too. The serve daemon uses StartGraceful so
// its first Ctrl-C can stop scheduling while letting claimed reviewers finish.
func (s *Scheduler) Start(ctx context.Context) error {
	return s.StartGraceful(ctx, ctx)
}

// StartGraceful runs the enabled loops until stopCtx is cancelled: discovery
// receives stopCtx and is cancelled immediately, while in-flight reviewers
// receive reviewCtx and drain unless that second context is cancelled too.
// Enabled loops fire immediately on start.
func (s *Scheduler) StartGraceful(stopCtx, reviewCtx context.Context) error {
	// A crashed daemon leaves a running run row (which would block cycles
	// for the whole lease window) and claimed queue rows (which would wait
	// it out too). Reconcile before the first tick so a restart resumes
	// immediately. Failure is logged, not fatal; the lease window is the
	// fallback that always works.
	reconcile := s.reconcile
	if reconcile == nil {
		reconcile = s.Reconcile
	}
	if err := reconcile(reviewCtx); err != nil {
		s.logf("reconcile: %v", err)
	}
	boot := s.cfg()
	loopRunner := s.loopRunner
	if loopRunner == nil {
		loopRunner = s.loop
	}
	var wg sync.WaitGroup
	started := false
	if boot.DiscoveryEnabled() {
		s.logf("scheduler: discovery every %s (config reloads live)", boot.DiscoverInterval())
		started = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			loopRunner(stopCtx, func() time.Duration { return s.cfg().DiscoverInterval() }, "discover", s.Discover)
		}()
	}
	if boot.ScheduleEnabled() {
		s.logf("scheduler: reviews every %s, max parallel %d (config reloads live)", boot.Interval(), boot.MaxParallel())
		started = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			loopRunner(stopCtx, func() time.Duration { return s.cfg().Interval() }, "review", func(context.Context) error {
				return s.reviewCycle(stopCtx, reviewCtx)
			})
		}()
	}
	if !started {
		<-stopCtx.Done()
		return stopCtx.Err()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return stopCtx.Err()
	case <-reviewCtx.Done():
		return reviewCtx.Err()
	}
}

// loopHeartbeat is how often a loop re-reads its interval, so a cadence edit
// in config.json takes effect within this bound instead of after the
// previously scheduled tick.
const loopHeartbeat = 30 * time.Second

// due reports whether interval has elapsed since the last run started. The
// heartbeat evaluates it against the LIVE interval, so shrinking the cadence
// in config.json can make an already-elapsed run due on the next beat.
func due(last, now time.Time, interval time.Duration) bool {
	return now.Sub(last) >= interval
}

// loop runs fn immediately, then whenever interval() has elapsed since the
// last run started.
func (s *Scheduler) loop(ctx context.Context, interval func() time.Duration, name string, fn func(context.Context) error) {
	run := func() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := fn(ctx); err != nil {
			s.logf("%s error: %v", name, err)
		}
	}
	last := time.Now()
	run()
	ticker := time.NewTicker(loopHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if due(last, time.Now(), interval()) {
				last = time.Now()
				run()
			}
		}
	}
}
