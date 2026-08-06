//go:build !unix

package review

import "os/exec"

// detachFromTerminalSignals is a no-op off Unix. Process groups and the
// terminal signal semantics this guards against (see the unix build) are a
// POSIX concept; Windows uses console control events and a different job
// model, and the release pipeline ships macOS and Linux only. The file exists
// so the platform assumption is stated rather than assumed by a syscall that
// happens not to compile elsewhere.
func detachFromTerminalSignals(cmd *exec.Cmd) {}
