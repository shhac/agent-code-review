//go:build unix

package review

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestDetachFromTerminalSignalsLeavesOurProcessGroup pins the property that
// keeps a Ctrl-C from killing a review that is minutes and a million tokens
// in: the engine subprocess must NOT share our process group, because a
// terminal delivers SIGINT to the whole foreground group and a child inherits
// its parent's group by default.
//
// The assertion is on the group id rather than on an actual signal, because
// signalling our own group is what a terminal does and would take the test
// binary down with it. The contrast case is asserted too: without the helper
// the child DOES share our group, so this test fails if the helper silently
// stops doing anything, rather than passing for the wrong reason.
func TestDetachFromTerminalSignalsLeavesOurProcessGroup(t *testing.T) {
	ours, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatalf("Getpgid(self): %v", err)
	}

	start := func(detach bool) int {
		t.Helper()
		// CommandContext, as every production call site uses.
		cmd := exec.CommandContext(t.Context(), "sleep", "30")
		if detach {
			detachFromTerminalSignals(cmd)
		}
		if err := cmd.Start(); err != nil {
			t.Fatalf("start: %v", err)
		}
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
		pgid, err := syscall.Getpgid(cmd.Process.Pid)
		if err != nil {
			t.Fatalf("Getpgid(child): %v", err)
		}
		return pgid
	}

	if pgid := start(true); pgid == ours {
		t.Errorf("detached child is in our process group (%d); a terminal Ctrl-C would kill it mid-review", pgid)
	}
	// Without the helper the child shares our group. If this ever stops being
	// true the assertion above proves nothing, so it is checked rather than
	// assumed.
	if pgid := start(false); pgid != ours {
		t.Errorf("undetached child pgid = %d, want ours (%d); the test above is no longer meaningful", pgid, ours)
	}
}

// TestDetachedEngineStillDiesOnContextCancel pins the other half of the
// bargain: detaching must not make a review unkillable. The forced shutdown
// (the SECOND Ctrl-C) cancels reviewCtx, and that has to reach the engine even
// though it now sits in its own group.
func TestDetachedEngineStillDiesOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "sleep", "30")
	detachFromTerminalSignals(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	cancel()
	select {
	case <-done:
		// Killed by the Cancel hook, which is the point.
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("a detached engine survived context cancellation; a forced shutdown would hang")
	}
}
