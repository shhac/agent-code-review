package scheduler

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"

	"github.com/shhac/agent-code-review/internal/store"
)

// Reconcile cleans up after crashed processes on THIS host: queue claims
// whose recorded pid is dead are released immediately instead of waiting out
// the lease window (2h+ of a PR sitting untouchable after every mid-review
// crash, which bites hardest during development). Another host's state (and
// any live pid's) is left strictly alone: a sibling instance's in-flight work
// looks exactly like this, minus the dead pid.
//
// With no global run-lock, this is the only fast reclaim path: the lease
// window is the fallback that always works, but only eventually.
func (s *Scheduler) Reconcile(ctx context.Context) error {
	host := hostname()

	queue, err := s.store.ListQueue(ctx, "")
	if err != nil {
		return err
	}
	for _, c := range queue {
		if c.ClaimedAt == nil || c.ClaimHost != host || s.pidAlive(c.ClaimPID) {
			continue
		}
		s.logf("reconcile: %s#%d was claimed by dead pid %d, releasing", c.Repo, c.Number, c.ClaimPID)
		// Record the abandoned attempt BEFORE releasing it. Without this the
		// interruption leaves no trace at all: no history row, and a work_dir
		// about to be overwritten by the next claim, so the transcript of a
		// review that may have spent minutes and dollars becomes unreachable.
		// Recorded first so a crash in between costs a duplicate row rather
		// than a silent loss.
		if err := s.store.AppendHistory(ctx, abandonedRecord(c)); err != nil {
			return err
		}
		if err := s.store.ClearClaim(ctx, c.Repo, c.Number); err != nil {
			return err
		}
	}
	return nil
}

// pidAlive is the production liveness probe: signal 0 reaches any process we
// can address. EPERM means "alive but not ours": still alive. Non-positive
// pids (missing data) count as dead rather than blocking reconciliation.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

// abandonedRecord is the history row for a review the daemon never finished.
// ERROR rather than a real verdict on purpose: an abandoned attempt must not
// count as "reviewed at this SHA", so the PR stays eligible and the queue row
// it leaves behind is picked up again.
//
// The engine is recorded as abandoned rather than guessed: which engine ran is
// not on the queue row, and the configured one may have changed since. The
// work_dir is what matters, and it keeps the transcript reachable.
func abandonedRecord(c store.Candidate) store.Review {
	started := time.Time{}
	if c.ClaimedAt != nil {
		started = *c.ClaimedAt
	}
	rec := store.ReviewFrom(c, store.VerdictError, store.EngineAbandoned, started)
	rec.WorkDir = c.WorkDir
	return rec
}
