// The interval loop: run a function immediately, then whenever its live
// interval has elapsed. Discovery is the only user; reviews are dispatched
// continuously rather than on a tick.

package scheduler

import (
	"context"
	"time"
)

// loopHeartbeat is how often a loop re-reads its interval, so a cadence edit
// in config.json takes effect within this bound instead of after the
// previously scheduled tick. New copies it onto the Scheduler's heartbeat
// seam; tests shrink theirs to drive the loop without real 30s waits.
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
	ticker := time.NewTicker(s.heartbeat)
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
