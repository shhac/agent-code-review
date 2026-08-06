package scheduler

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
	"github.com/shhac/agent-code-review/internal/usage"
)

// reviewCycleOnce processes the queued candidates with one context standing
// in for both stop and review cancellation: RunCycle's (and the tests')
// entry. It is a no-op (returns nil) when another cycle is still in flight:
// the run-lock rule from the spec. An idle cycle (nothing available to
// review) exits before the run-lock and records nothing: with the default 1m
// cadence, anything else would flood the runs table and the log with empty
// ticks.
func (s *Scheduler) reviewCycleOnce(ctx context.Context) error {
	return s.reviewCycle(ctx, ctx)
}

func (s *Scheduler) reviewCycle(stopCtx, reviewCtx context.Context) error {
	select {
	case <-stopCtx.Done():
		return nil
	default:
	}
	cfg := s.cfg()

	staleAfter := cfg.LeaseWindow()
	queue, err := s.store.ListQueue(reviewCtx, "")
	if err != nil {
		return err
	}
	available := availableCandidates(queue, time.Now(), staleAfter)
	if len(available) == 0 {
		return nil
	}

	// Each author's policy decides which engine reviews their PR, so it is
	// resolved once here and threaded down: the usage floor, the engine build,
	// and the prompt then all read the same answer.
	pending, err := s.resolvePending(reviewCtx, cfg, available)
	if err != nil {
		return err
	}
	runnable := s.dropFlooredEngines(cfg, pending)
	// Every candidate is waiting on a floored engine. Exit before the run-lock
	// so a fully paused cycle still records no run, which is what kept the runs
	// table free of empty ticks when the floor was a whole-cycle gate.
	if len(runnable) == 0 {
		return nil
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

	s.logf("cycle: %d candidate(s) to review", len(runnable))
	s.processQueue(stopCtx, reviewCtx, runnable, cfg)
	return nil
}

// resolvePending pairs each candidate with its author's resolved policy.
// A roster lookup that fails aborts the cycle rather than defaulting: guessing
// a policy is how a PR gets approved that shouldn't be.
func (s *Scheduler) resolvePending(ctx context.Context, cfg config.Config, candidates []store.Candidate) ([]pending, error) {
	out := make([]pending, 0, len(candidates))
	for _, c := range candidates {
		membership, err := s.store.AuthorGroup(ctx, c.Repo, c.Author)
		if err != nil {
			return nil, fmt.Errorf("resolve author group for %s#%d: %w", c.Repo, c.Number, err)
		}
		out = append(out, pending{candidate: c, policy: cfg.ResolvePolicy(c.Repo, c.Author, membership)})
	}
	return out, nil
}

// dropFlooredEngines removes candidates whose engine has too little headroom
// left, leaving room for interactive work on that account.
//
// This is a HOLD, not a skip: a dropped candidate is never claimed, completed,
// or recorded, so it simply waits in the queue like a cooldown or a settling
// hold and runs the moment its window refills. Nothing is lost by dropping it,
// which is why the floor can be per candidate rather than per cycle: an engine
// that is out of headroom stops its own work without stopping the other's.
func (s *Scheduler) dropFlooredEngines(cfg config.Config, candidates []pending) []pending {
	runnable := make([]pending, 0, len(candidates))
	floored := map[string]int{}
	for _, p := range candidates {
		engine := cfg.EngineFor(p.policy)
		paused, reason := usage.BelowFloor(s.usageFn(engine), cfg.UsageFloor5h(), cfg.UsageFloorWeekly())
		if !paused {
			runnable = append(runnable, p)
			continue
		}
		if floored[engine] == 0 {
			// Once per engine per cycle: "why is nothing moving" must be
			// answerable from the log without one line per waiting PR.
			s.logf("cycle: %s is at its usage floor (%s), holding its candidates", engine, reason)
		}
		floored[engine]++
	}
	return runnable
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
	return s.reviewCycleOnce(ctx)
}
