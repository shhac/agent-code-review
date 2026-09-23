package scheduler

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shhac/crew-code-review/internal/config"
)

// sweepFixture gives one test its own state dir and a scheduler whose clock
// and retention it controls. SweepWorkspaces is the one function in this
// package that deletes transcripts, so every case runs against a directory
// nothing else writes to.
func sweepFixture(t *testing.T, retention string) (*Scheduler, string, time.Time) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	cfg := config.Config{Review: config.ReviewSettings{WorkspaceRetention: retention}}
	s := newScheduler(Deps{
		Config: func() config.Config { return cfg },
		Now:    func() time.Time { return now },
	})
	return s, cfg.ReviewWorkspaceDir(), now
}

// agedWorkspace makes a directory under base with the given age, holding an
// agent log so a removal is a removal of real content rather than of an empty
// dir.
func agedWorkspace(t *testing.T, base, name string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.log"), []byte("session id: s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// After the write, which would otherwise bump the directory's mtime back
	// to now.
	if err := os.Chtimes(dir, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return dir
}

func pathExists(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Stat(p)
	if err == nil {
		return true
	}
	if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return false
}

func TestSweepWorkspaces(t *testing.T) {
	const day = 24 * time.Hour

	t.Run("removes what is past retention and keeps what is not", func(t *testing.T) {
		s, base, now := sweepFixture(t, "72h")
		old := agedWorkspace(t, base, "5-old", now.Add(-4*day))
		atCutoff := agedWorkspace(t, base, "6-edge", now.Add(-3*day))
		fresh := agedWorkspace(t, base, "7-fresh", now.Add(-time.Hour))

		n, err := s.SweepWorkspaces()
		if err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Errorf("removed = %d, want 2 (the old one and the one exactly at the cutoff)", n)
		}
		if pathExists(t, old) || pathExists(t, atCutoff) {
			t.Error("workspaces at or past retention must be removed, contents and all")
		}
		if !pathExists(t, fresh) {
			t.Error("a workspace inside retention is somebody's recent transcript and must survive")
		}
	})

	t.Run("plain files are not workspaces", func(t *testing.T) {
		s, base, now := sweepFixture(t, "72h")
		if err := os.MkdirAll(base, 0o700); err != nil {
			t.Fatal(err)
		}
		stray := filepath.Join(base, "notes.txt")
		if err := os.WriteFile(stray, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(stray, now.Add(-30*day), now.Add(-30*day)); err != nil {
			t.Fatal(err)
		}

		n, err := s.SweepWorkspaces()
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 || !pathExists(t, stray) {
			t.Errorf("removed = %d, stray kept = %v: only directories are swept", n, pathExists(t, stray))
		}
	})

	t.Run("a zero retention keeps everything", func(t *testing.T) {
		// "0s" is the documented way to keep workspaces forever, not a
		// retention of nothing: durationOrZero keeps an explicit zero.
		s, base, now := sweepFixture(t, "0s")
		ancient := agedWorkspace(t, base, "5-ancient", now.Add(-365*day))

		n, err := s.SweepWorkspaces()
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 || !pathExists(t, ancient) {
			t.Errorf("removed = %d: a zero retention must remove nothing", n)
		}
	})

	t.Run("a negative retention is the default, not keep-forever", func(t *testing.T) {
		// Pinned because it is easy to assume otherwise: only an explicit zero
		// disables the sweep. A negative duration fails durationOrZero's
		// parse-or-default rule and reads as the 30 day default.
		s, base, now := sweepFixture(t, "-1h")
		old := agedWorkspace(t, base, "5-old", now.Add(-31*day))
		recent := agedWorkspace(t, base, "6-recent", now.Add(-29*day))

		n, err := s.SweepWorkspaces()
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 || pathExists(t, old) || !pathExists(t, recent) {
			t.Errorf("removed = %d: want the 30 day default applied", n)
		}
	})

	t.Run("no workspace dir yet is not an error", func(t *testing.T) {
		// A daemon that has never run a review has nothing to sweep.
		s, base, _ := sweepFixture(t, "72h")
		if pathExists(t, base) {
			t.Fatalf("fixture must start without %s", base)
		}
		n, err := s.SweepWorkspaces()
		if n != 0 || err != nil {
			t.Errorf("SweepWorkspaces() = (%d, %v), want (0, nil)", n, err)
		}
	})
}
