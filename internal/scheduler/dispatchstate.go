package scheduler

import (
	"fmt"
	"time"

	"github.com/shhac/agent-code-review/internal/store"
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

// flooredLogEvery throttles the per-engine "at its usage floor" line. It has
// its own constant rather than borrowing the idle poll: an engine can sit
// below its floor for hours, and at a 10s poll that would be 360 identical
// lines an hour into a 1000-entry log ring. Five minutes still answers "why is
// nothing moving" from the log without burying what else happened.
const flooredLogEvery = 5 * time.Minute

func candidateKey(c store.Candidate) string { return fmt.Sprintf("%s#%d", c.Repo, c.Number) }

// candidateState is everything the dispatcher remembers about ONE candidate.
// It was three maps keyed alike (in-flight, failure count, hold expiry), which
// is the shape where one code path forgets one map: `finish` had to delete
// from three and `skip` had to read two.
type candidateState struct {
	inFlight  bool
	fails     int
	holdUntil time.Time
}

// dispatchState is the dispatcher's private bookkeeping. Only the dispatch
// goroutine touches it (workers report completion over a channel, and the
// dispatcher applies it), so it needs no lock. `make test-race` is what holds
// that claim honest.
//
// now is a seam: the backoff it hands out is measured in minutes to hours, so
// without an injectable clock no test can reach an expiry and "a held
// candidate is eventually offered again" goes unpinned.
type dispatchState struct {
	byKey map[string]*candidateState
	now   func() time.Time

	// floorLogged rate-limits the per-ENGINE "at its usage floor" line. A
	// different key space from byKey, and deliberately separate: it is a log
	// throttle, not candidate bookkeeping.
	floorLogged map[string]time.Time
}

func newDispatchState(now func() time.Time) *dispatchState {
	return &dispatchState{
		byKey:       map[string]*candidateState{},
		now:         now,
		floorLogged: map[string]time.Time{},
	}
}

func (d *dispatchState) entry(key string) *candidateState {
	c, ok := d.byKey[key]
	if !ok {
		c = &candidateState{}
		d.byKey[key] = c
	}
	return c
}

// active is how many reviews are in flight. Derived rather than counted: a
// separate counter alongside inFlight is one number stored twice, and the two
// can only ever disagree.
func (d *dispatchState) active() int {
	n := 0
	for _, c := range d.byKey {
		if c.inFlight {
			n++
		}
	}
	return n
}

// skip reports whether a candidate is unavailable to this dispatcher right
// now: already handed to a worker, or serving a failure backoff. Both are
// in-process concerns; cross-process exclusion is the claim CAS in reviewOne.
func (d *dispatchState) skip(key string) bool {
	c, ok := d.byKey[key]
	return ok && (c.inFlight || d.now().Before(c.holdUntil))
}

func (d *dispatchState) start(key string) { d.entry(key).inFlight = true }

// finish clears the dispatch and either forgets the candidate or extends its
// backoff. A failure here means "the attempt did not end in a recorded
// outcome", which is exactly the case where the queue row may still be sitting
// at the head waiting to be offered again.
func (d *dispatchState) finish(key string, failed bool) {
	if !failed {
		delete(d.byKey, key)
		return
	}
	c := d.entry(key)
	c.inFlight = false
	d.fail(key)
}

// fail extends a candidate's backoff. Separate from finish because a candidate
// can fail before it is ever dispatched (its roster lookup errors), and that
// must hold it back just the same.
func (d *dispatchState) fail(key string) {
	c := d.entry(key)
	c.fails++
	c.holdUntil = d.now().Add(backoffFor(c.fails))
}

// prune forgets candidates that are no longer queued and not in flight. Their
// backoff entries would otherwise accumulate for the life of the process: a
// candidate can leave the queue by routes the dispatcher never sees (merged,
// dequeued from the dashboard, promoted away, completed by a sibling daemon).
// Bounded by "distinct PRs that ever failed", so small, but a daemon runs for
// weeks.
func (d *dispatchState) prune(queued map[string]bool) {
	for key, c := range d.byKey {
		if !c.inFlight && !queued[key] {
			delete(d.byKey, key)
		}
	}
}

// backoffFor doubles from the base with each consecutive failure, capped.
func backoffFor(fails int) time.Duration {
	if fails < 1 {
		return dispatchBackoffBase
	}
	// Past this the shift would overflow, and the cap has long since applied.
	if fails > 63 {
		return dispatchBackoffCap
	}
	return min(dispatchBackoffBase<<(fails-1), dispatchBackoffCap)
}

// logFloored rate-limits the per-engine "at its usage floor" line: "why is
// nothing moving" has to be answerable from the log without one line per pull.
// Replaces the batch cycle's once-per-cycle map, which had a cycle to key on.
func (d *dispatchState) logFloored(engine string, every time.Duration) bool {
	now := d.now()
	if last, ok := d.floorLogged[engine]; ok && now.Sub(last) < every {
		return false
	}
	d.floorLogged[engine] = now
	return true
}
