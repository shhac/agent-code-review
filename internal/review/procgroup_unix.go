//go:build unix

package review

import (
	"os/exec"
	"syscall"
)

// detachFromTerminalSignals puts the engine subprocess in its OWN process
// group, and makes context cancellation tear that whole group down.
//
// Without this the daemon cannot keep the promise it prints on the first
// Ctrl-C ("waiting for in-flight reviewers"). A terminal delivers SIGINT to
// the entire FOREGROUND PROCESS GROUP, not just the process you started, and
// a child inherits its parent's group by default. So Ctrl-C reached the
// engine directly, killing a review that was minutes and over a million
// tokens in, before any of the shutdown wiring was consulted. The contexts
// were never the problem: the signal simply arrived somewhere else first.
// Every such review was recorded as ERROR with its spend already gone.
//
// It follows that the graceful/force split is only meaningful once the engine
// is out of the terminal's reach. In its own group the engine sees no SIGINT,
// so the first signal stops new work and lets the current review finish, and
// the second one cancels reviewCtx, which is what this Cancel hook turns into
// a kill.
//
// The kill targets the NEGATIVE pid, i.e. the group: engines spawn their own
// children (a shell, a language toolchain, gh), and killing only the leader
// would strand them holding the pipe we are still reading. WaitDelay bounds
// exactly that case, so a wedged descendant cannot hang shutdown forever.
func detachFromTerminalSignals(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Only CommandContext populates Cancel, and os/exec rejects a non-nil
	// Cancel on a command built without one. Replacing it rather than setting
	// it keeps this helper safe for a caller that has no context to cancel.
	if cmd.Cancel == nil {
		return
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = engineWaitDelay
}
