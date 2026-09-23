package scheduler

// Review workspaces: making one per claim, and retiring old ones.
//
// Nothing ever removed these. They stopped growing only because the system
// temp directory was being swept by the OS, which is the very behaviour that
// made 92% of recorded transcripts unreadable. Moving them somewhere durable
// means owning their lifetime, from the claim that creates one to the sweep
// that removes it.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shhac/crew-code-review/internal/config"
	"github.com/shhac/crew-code-review/internal/store"
)

// claimWorkspace creates this attempt's workspace and claims the candidate
// with it recorded. claimed is false when another worker won the
// compare-and-swap; either that or an error leaves no directory behind.
//
// The workdir exists before the claim so the claim can record it: from
// that moment <work_dir>/agent.log is the candidate's live review log.
//
// Under the app's STATE dir rather than the system temp dir. MkdirTemp("")
// put these where macOS sweeps them, so the transcript a history row
// points at was usually gone by the time anyone looked: the log is the
// only record of what the agent actually did, and it was being kept
// somewhere designed to lose it.
func (s *Scheduler) claimWorkspace(ctx context.Context, cfg config.Config, c store.Candidate) (workDir string, claimedAt time.Time, claimed bool, err error) {
	base := cfg.ReviewWorkspaceDir()
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", time.Time{}, false, err
	}
	workDir, err = os.MkdirTemp(base, fmt.Sprintf("%d-", c.Number))
	if err != nil {
		return "", time.Time{}, false, err
	}
	claimedAt = time.Now()
	claimed, err = s.store.Claim(ctx, c.Repo, c.Number, store.Lease{
		At: claimedAt, WorkDir: workDir, Host: hostname(), PID: os.Getpid(), StaleAfter: cfg.LeaseWindow(),
	})
	if err != nil || !claimed {
		// The directory only earns its keep once a claim records it: nothing
		// points at this one, so nothing would ever read or remove it. One
		// cleanup for both ways of not holding the claim; when they were
		// separate, the error path returned straight past its copy and leaked
		// a directory per failure.
		_ = os.Remove(workDir)
		return "", time.Time{}, false, err
	}
	return workDir, claimedAt, true, nil
}

// SweepWorkspaces removes review workspaces older than the configured
// retention, returning how many it removed.
//
// Age is taken from the directory's modification time, which the agent log
// keeps current for as long as the review is writing to it. That is what makes
// this safe to run at boot without consulting the queue: a workspace still
// being written to is by definition recent, and retention is measured in days
// against reviews measured in minutes. A review somehow older than the whole
// retention window has been abandoned for weeks and its claim lease expired
// long ago.
//
// Errors on individual entries are skipped rather than aborting the sweep: one
// unreadable directory must not strand every later one.
func (s *Scheduler) SweepWorkspaces() (int, error) {
	cfg := s.cfg()
	retention := cfg.WorkspaceRetention()
	if retention <= 0 {
		return 0, nil
	}
	base := cfg.ReviewWorkspaceDir()
	entries, err := os.ReadDir(base)
	if err != nil {
		// A daemon that has never run a review has no directory yet, which is
		// not a problem to report.
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	cutoff := s.now().Add(-retention)
	removed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(base, e.Name())); err == nil {
			removed++
		}
	}
	return removed, nil
}
