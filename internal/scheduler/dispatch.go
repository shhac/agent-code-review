package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
	"github.com/shhac/agent-code-review/internal/usage"
)

// dispatchBackoffBase and dispatchBackoffCap bound the per-candidate hold
// applied after a review fails. The dispatcher always offers the queue's head
// first, so a candidate that fails BEFORE its claim (an unbuildable engine, a
// workdir that cannot be created, a roster lookup that errors) leaves its row
// exactly as it found it and would otherwise be re-offered forever, blocking
// every candidate behind it. The batch cycle this replaced never had that
// problem: it walked a snapshot, so a failing candidate cost one slot and the
// rest of the batch still ran.
const (
	dispatchBackoffBase = time.Minute
	dispatchBackoffCap  = time.Hour
)

// dispatchState is the dispatcher's private bookkeeping. Only the dispatch
// goroutine touches it (workers report completion over a channel, and the
// dispatcher applies it), so it needs no lock.
type dispatchState struct {
	inFlight  map[string]bool      // dispatched, not yet finished
	fails     map[string]int       // consecutive failures, per candidate
	holdUntil map[string]time.Time // backoff expiry, per candidate
	flooredAt map[string]time.Time // last "at its usage floor" log, per engine
}

func newDispatchState() *dispatchState {
	return &dispatchState{
		inFlight:  map[string]bool{},
		fails:     map[string]int{},
		holdUntil: map[string]time.Time{},
		flooredAt: map[string]time.Time{},
	}
}

func candidateKey(c store.Candidate) string { return fmt.Sprintf("%s#%d", c.Repo, c.Number) }

// skip reports whether a candidate is unavailable to this dispatcher right
// now: already handed to a worker, or serving a failure backoff. Both are
// in-process concerns; cross-process exclusion is the claim CAS in reviewOne.
func (d *dispatchState) skip(key string, now time.Time) bool {
	return d.inFlight[key] || now.Before(d.holdUntil[key])
}

func (d *dispatchState) start(key string) { d.inFlight[key] = true }

// finish clears the dispatch and either resets or extends the candidate's
// backoff. A failure here means "the attempt did not end in a recorded
// outcome", which is exactly the case where the queue row may still be
// sitting at the head waiting to be offered again.
func (d *dispatchState) finish(key string, failed bool) {
	delete(d.inFlight, key)
	if !failed {
		delete(d.fails, key)
		delete(d.holdUntil, key)
		return
	}
	d.fail(key)
}

// fail extends a candidate's backoff. Separate from finish because a
// candidate can fail before it is ever dispatched (its roster lookup errors),
// and that must hold it back just the same.
func (d *dispatchState) fail(key string) {
	d.fails[key]++
	d.holdUntil[key] = time.Now().Add(backoffFor(d.fails[key]))
}

// backoffFor doubles from the base with each consecutive failure, capped.
func backoffFor(fails int) time.Duration {
	wait := dispatchBackoffBase
	for i := 1; i < fails && wait < dispatchBackoffCap; i++ {
		wait *= 2
	}
	if wait > dispatchBackoffCap {
		return dispatchBackoffCap
	}
	return wait
}

// logFloored rate-limits the per-engine "at its usage floor" line: "why is
// nothing moving" has to be answerable from the log without one line per
// pull. Replaces the batch cycle's once-per-cycle map, which had a cycle to
// key on.
func (d *dispatchState) logFloored(engine string, every time.Duration) bool {
	now := time.Now()
	if last, ok := d.flooredAt[engine]; ok && now.Sub(last) < every {
		return false
	}
	d.flooredAt[engine] = now
	return true
}

// finishedReview carries one completed hand-off back to the dispatcher.
type finishedReview struct {
	key    string
	failed bool
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
// gracefulCtx stops new dispatches; reviewCtx cancels reviews already
// running. With drain set (the `run` one-shot) the loop returns once nothing
// is dispatchable and no worker is in flight, instead of idling, and a failed
// pull aborts rather than being logged and retried: a cron run whose store is
// unreadable has to exit non-zero, while a daemon has to keep trying.
func (s *Scheduler) dispatch(gracefulCtx, reviewCtx context.Context, drain bool) error {
	state := newDispatchState()
	finished := make(chan finishedReview, 1)
	active := 0

	// Every return path waits for the workers: a dispatcher that outlived its
	// reviewers would let StartGraceful report the loops drained while engine
	// subprocesses were still running.
	defer func() {
		for active > 0 {
			d := <-finished
			state.finish(d.key, d.failed)
			active--
		}
	}()

	for {
		cfg := s.cfg()

		// At capacity: the only useful thing left is to reap. MaxParallel is
		// re-read after each completion so raising it mid-flight takes effect
		// without a restart (lowering it takes effect as slots free).
		for active >= cfg.MaxParallel() {
			select {
			case d := <-finished:
				state.finish(d.key, d.failed)
				active--
				cfg = s.cfg()
			case <-gracefulCtx.Done():
				s.logf("dispatch: shutdown requested, waiting for in-flight reviewer(s)")
				return nil
			case <-reviewCtx.Done():
				return nil
			}
		}
		// Reap anything else that finished while we were dispatching, so the
		// in-flight set does not hold keys whose reviews are long over.
		for reaping := true; reaping; {
			select {
			case d := <-finished:
				state.finish(d.key, d.failed)
				active--
			default:
				reaping = false
			}
		}

		// Checked BEFORE the pull, and again below, because a select with a
		// ready case chooses uniformly at random: leaving cancellation to the
		// selects alone started a new review roughly half the time after a
		// shutdown was requested, at the cost of a whole engine invocation
		// each time. Same reasoning the batch loop carried.
		if gracefulCtx.Err() != nil {
			s.logf("dispatch: shutdown requested, waiting for in-flight reviewer(s)")
			return nil
		}
		if reviewCtx.Err() != nil {
			return nil
		}

		next, err := s.pullNext(reviewCtx, cfg, state)
		if err != nil {
			if drain {
				return err
			}
			s.logf("dispatch: %v", err)
		}
		if next == nil {
			if drain && active == 0 {
				return nil
			}
			// Waking on a completion rather than only on the timer keeps the
			// idle poll from delaying work that a finishing review unblocks.
			select {
			case d := <-finished:
				state.finish(d.key, d.failed)
				active--
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
		if gracefulCtx.Err() != nil {
			s.logf("dispatch: shutdown requested, waiting for in-flight reviewer(s)")
			return nil
		}
		if reviewCtx.Err() != nil {
			return nil
		}

		key := candidateKey(next.candidate)
		state.start(key)
		active++
		go func(p pending) {
			finished <- finishedReview{key: key, failed: s.runOne(reviewCtx, p) != nil}
		}(*next)

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
	now := time.Now()
	for _, c := range availableCandidates(queue, now, cfg.LeaseWindow()) {
		key := candidateKey(c)
		if state.skip(key, now) {
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
