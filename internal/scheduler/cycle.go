package scheduler

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/shhac/agent-code-review/internal/store"
	"github.com/shhac/agent-code-review/internal/usage"
)

// ReviewCycle processes the queued candidates. It is a no-op (returns nil)
// when another cycle is still in flight: the run-lock rule from the spec.
// An idle cycle (nothing available to review) exits before the run-lock and
// records nothing: with the default 1m cadence, anything else would flood the
// runs table and the log with empty ticks.
func (s *Scheduler) ReviewCycle(ctx context.Context) error {
	return s.reviewCycle(ctx, ctx)
}

func (s *Scheduler) reviewCycle(stopCtx, reviewCtx context.Context) error {
	// Usage floor: leave headroom in the Codex windows for interactive work.
	// Checked before the run-lock so a paused cycle records no run. The loop
	// keeps ticking, so reviews resume as soon as the window refills.
	select {
	case <-stopCtx.Done():
		return nil
	default:
	}
	cfg := s.cfg()
	if paused, reason := usage.BelowFloor(s.usageFn(), cfg.UsageFloor5h(), cfg.UsageFloorWeekly()); paused {
		s.logf("cycle: paused by usage floor (%s)", reason)
		return nil
	}

	staleAfter := cfg.LeaseWindow()
	queue, err := s.store.ListQueue(reviewCtx, "")
	if err != nil {
		return err
	}
	available := availableCandidates(queue, time.Now(), staleAfter)
	if len(available) == 0 {
		return nil
	}

	engine, err := s.newEngine(cfg)
	if err != nil {
		return fmt.Errorf("build review engine: %w", err)
	}

	s.logf("cycle: started at %s", time.Now().Format(time.RFC3339))

	if _, active, err := s.store.ActiveRun(reviewCtx, staleAfter); err != nil {
		return err
	} else if active {
		s.logf("cycle: a previous run is still active, skipping")
		return nil
	}

	run := store.Run{ID: newRunID(), StartedAt: time.Now(), Host: hostname(), PID: os.Getpid()}
	if err := s.store.StartRun(reviewCtx, run); err != nil {
		return err
	}
	status := "done"
	defer func() {
		if err := s.store.FinishRun(reviewCtx, run.ID, status); err != nil {
			s.logf("cycle: finish run: %v", err)
		}
		s.logf("cycle: finished at %s (%s)", time.Now().Format(time.RFC3339), status)
	}()

	s.logf("cycle: %d candidate(s) to review", len(available))
	s.processQueue(stopCtx, reviewCtx, available, cfg, engine)
	return nil
}

// availableCandidates filters the queue to rows that are actually reviewable
// right now: no live lease (see store.Candidate.ClaimActive: a fresh claim
// is another worker mid-review; a stale one is a crashed daemon's abandoned
// lease, reclaimed here) and no eligibility hold (store.Candidate.Held:
// cooling down after a recent review, or settling after a fresh push). Pure.
// The boundary is unit-tested directly.
func availableCandidates(queue []store.Candidate, now time.Time, staleAfter time.Duration) []store.Candidate {
	out := make([]store.Candidate, 0, len(queue))
	for _, c := range queue {
		if !c.ClaimActive(now, staleAfter) && !c.Held(now) {
			out = append(out, c)
		}
	}
	return out
}

// RunCycle is the one-shot flow (`run --once`): reconcile leftovers, then a
// discovery sweep followed by one review cycle.
func (s *Scheduler) RunCycle(ctx context.Context) error {
	if err := s.Reconcile(ctx); err != nil {
		s.logf("reconcile: %v", err)
	}
	if err := s.Discover(ctx); err != nil {
		return err
	}
	return s.ReviewCycle(ctx)
}
