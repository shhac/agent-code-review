package scheduler

import (
	"context"
	"errors"
	"os"
	"syscall"
)

// Reconcile cleans up after crashed processes on THIS host: run rows still
// marked running and queue claims whose recorded pid is dead are released
// immediately instead of waiting out the lease window (2h+ of "a previous
// run is still active — skipping" after every mid-cycle crash, which bites
// hardest during development). Another host's state — and any live pid's —
// is left strictly alone: a sibling instance's in-flight work looks exactly
// like this, minus the dead pid.
func (s *Scheduler) Reconcile(ctx context.Context) error {
	host := hostname()

	runs, err := s.store.RunningRuns(ctx)
	if err != nil {
		return err
	}
	for _, r := range runs {
		if r.Host != host || s.pidAlive(r.PID) {
			continue
		}
		s.logf("reconcile: run %s (pid %d) died mid-cycle — marking failed", r.ID, r.PID)
		if err := s.store.FinishRun(ctx, r.ID, "failed"); err != nil {
			return err
		}
	}

	queue, err := s.store.ListQueue(ctx, "")
	if err != nil {
		return err
	}
	for _, c := range queue {
		if c.ClaimedAt == nil || c.ClaimHost != host || s.pidAlive(c.ClaimPID) {
			continue
		}
		s.logf("reconcile: %s#%d was claimed by dead pid %d — releasing", c.Repo, c.Number, c.ClaimPID)
		if err := s.store.ClearClaim(ctx, c.Repo, c.Number); err != nil {
			return err
		}
	}
	return nil
}

// pidAlive is the production liveness probe: signal 0 reaches any process we
// can address. EPERM means "alive but not ours" — still alive. Non-positive
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
