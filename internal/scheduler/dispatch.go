package scheduler

import (
	"context"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
	"github.com/shhac/agent-code-review/internal/usage"
)

// finishedReview carries one completed hand-off back to the dispatcher.
type finishedReview struct {
	key    string
	failed bool
}

// stopMsg is the one wording for a graceful stop, so the three places that
// can observe one cannot drift apart.
const stopMsg = "dispatch: shutdown requested, waiting for in-flight reviewer(s)"

// stopping reports whether either context has ended, logging the graceful
// case. It is called with an explicit Err() check rather than left to a
// select, because a select with a ready case chooses uniformly at random:
// leaving cancellation to the selects alone started a new review roughly half
// the time after a shutdown was requested, at the cost of a whole engine
// invocation each time.
func (s *Scheduler) stopping(gracefulCtx, reviewCtx context.Context) bool {
	if gracefulCtx.Err() != nil {
		s.logf("%s", stopMsg)
		return true
	}
	return reviewCtx.Err() != nil
}

// dispatch is the review consumer: one goroutine pulling the next ready
// candidate off the queue and handing it to a worker, up to cfg.MaxParallel
// at a time. A freed slot takes the next candidate after
// cfg.DispatchCooldown rather than waiting for a batch to drain, which is the
// whole difference from the review CYCLE this replaced: nothing is
// snapshotted, so a PR that becomes ready mid-review (newly discovered, an
// expired hold, an engine back over its usage floor) is picked up by the next
// free slot instead of waiting out every in-flight review.
//
// The loop is deliberately one shape: harvest finished workers, decide
// whether to stop, pull if there is room, and otherwise wait. There is a
// single wait, and it always watches the idle timer as well as the completion
// channel — including while at capacity, which is what makes a mid-flight
// max_parallel raise take effect within one idle poll rather than only after
// some review happens to finish.
//
// gracefulCtx stops new dispatches; reviewCtx cancels reviews already
// running. With stopWhenIdle set (the `run` one-shot) the loop returns once
// nothing is dispatchable and no worker is in flight, instead of idling, and
// carries out the last pull error so a cron run whose store is unreadable
// exits non-zero.
func (s *Scheduler) dispatch(gracefulCtx, reviewCtx context.Context, stopWhenIdle bool) error {
	state := newDispatchState(s.now)
	finished := make(chan finishedReview, 1)
	var pullErr error

	// Every return path waits for the workers: a dispatcher that outlived its
	// reviewers would let StartGraceful report the loops drained while engine
	// subprocesses were still running.
	defer func() {
		for state.active() > 0 {
			d := <-finished
			state.finish(d.key, d.failed)
		}
	}()

	for {
		cfg := s.cfg()

		// Harvest whatever finished while we were elsewhere, so the in-flight
		// set never holds keys whose reviews are long over.
		for harvesting := true; harvesting; {
			select {
			case d := <-finished:
				state.finish(d.key, d.failed)
			default:
				harvesting = false
			}
		}

		if s.stopping(gracefulCtx, reviewCtx) {
			return nil
		}

		var next *pending
		if state.active() < cfg.MaxParallel() {
			var err error
			next, err = s.pullNext(reviewCtx, cfg, state)
			if err != nil {
				pullErr = err
				s.logf("dispatch: %v", err)
			}
		}

		if next == nil {
			if stopWhenIdle && state.active() == 0 {
				return pullErr
			}
			select {
			case d := <-finished:
				state.finish(d.key, d.failed)
			case <-time.After(cfg.Interval()):
			case <-gracefulCtx.Done():
				return nil
			case <-reviewCtx.Done():
				return nil
			}
			continue
		}

		// The second half of the check above. A pull is not instant: it lists
		// the queue and resolves an author policy, each a DuckDB subprocess
		// behind a global mutex. A stop that lands while it runs must discard
		// the result rather than hand it to a worker, or the first Ctrl-C
		// still starts one more review than it promised. The candidate is
		// simply left queued; nothing about it was touched.
		if s.stopping(gracefulCtx, reviewCtx) {
			return nil
		}

		key := candidateKey(next.candidate)
		state.start(key)
		go func(p pending, key string) {
			finished <- finishedReview{key: key, failed: s.runOne(reviewCtx, p) != nil}
		}(*next, key)

		if !sleepCtx(gracefulCtx, reviewCtx, cfg.DispatchCooldown()) {
			return nil
		}
	}
}

// sleepCtx waits out d, returning false if either context ended first. Every
// wait in the dispatcher goes through here: a bare time.Sleep would leave
// StartGraceful up to a full idle poll behind a Ctrl-C.
func sleepCtx(gracefulCtx, reviewCtx context.Context, d time.Duration) bool {
	if d <= 0 {
		return gracefulCtx.Err() == nil && reviewCtx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-gracefulCtx.Done():
		return false
	case <-reviewCtx.Done():
		return false
	}
}

// pullNext returns the next candidate to review, or nil when nothing is
// dispatchable right now. It resolves author policy LAZILY, one candidate at
// a time, and stops at the first that clears its engine's usage floor: the
// dispatcher only ever hands off one candidate, so resolving the whole queue
// to pick one would spend a DuckDB subprocess per row on every pull.
func (s *Scheduler) pullNext(ctx context.Context, cfg config.Config, state *dispatchState) (*pending, error) {
	queue, err := s.store.ListQueue(ctx, "")
	if err != nil {
		return nil, err
	}
	// Forget candidates that have left the queue by a route the dispatcher
	// never sees, so their backoff entries do not accumulate for the life of
	// the daemon.
	queued := make(map[string]bool, len(queue))
	for _, c := range queue {
		queued[candidateKey(c)] = true
	}
	state.prune(queued)

	for _, c := range availableCandidates(queue, state.now(), cfg.LeaseWindow()) {
		key := candidateKey(c)
		if state.skip(key) {
			continue
		}
		membership, err := s.store.AuthorGroup(ctx, c.Repo, c.Author)
		if err != nil {
			// Never guess a policy: guessing one is how a PR gets approved
			// that shouldn't be. The candidate is skipped, not defaulted, and
			// backs off so a persistently broken roster row cannot hold the
			// queue head. Loud, because nothing else would report it.
			// Rate-limited by the backoff itself: skip() above already
			// filtered out anything still holding, so this logs at most once
			// per backoff window.
			s.logf("dispatch: resolve author group for %s: %v, skipping", key, err)
			state.fail(key)
			continue
		}
		p := pending{
			candidate: c,
			policy:    cfg.ResolvePolicy(c.Repo, c.Author, membership),
			cfg:       cfg,
		}
		engine := cfg.EngineFor(p.policy)
		paused, reason := usage.BelowFloor(s.usageFn(engine), cfg.UsageFloor5h(), cfg.UsageFloorWeekly())
		if paused {
			// A HOLD, not a skip: the candidate is never claimed, completed
			// or recorded, so it simply waits like a cooldown hold and runs
			// the moment its window refills. Per engine, so an engine out of
			// headroom does not stop the other's work.
			if state.logFloored(engine, cfg.Interval()) {
				s.logf("dispatch: %s is at its usage floor (%s), holding its candidates", engine, reason)
			}
			continue
		}
		return &p, nil
	}
	return nil, nil
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
