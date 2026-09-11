package scheduler

// Retiring old review workspaces.
//
// Nothing ever removed these. They stopped growing only because the system
// temp directory was being swept by the OS, which is the very behaviour that
// made 92% of recorded transcripts unreadable. Moving them somewhere durable
// means owning their lifetime.

import (
	"os"
	"path/filepath"
)

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
