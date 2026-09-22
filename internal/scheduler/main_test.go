package scheduler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
)

// TestMain moves every XDG base directory into a throwaway root for the whole
// package. Review workspaces resolve through xdg.StateDir, so without this
// every reviewOne in these tests made a real directory under the developer's
// ~/.local/state, and every StartGraceful ran the boot sweep over their real
// transcripts. Overridden even when the caller already set the variables: a
// guard that trusts the environment is the one that fails on a plain `go test`.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "acr-scheduler-test-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "scheduler tests: isolating XDG dirs: %v\n", err)
		os.Exit(1)
	}
	for _, env := range []string{"XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME"} {
		if err := os.Setenv(env, filepath.Join(root, strings.ToLower(env))); err != nil {
			fmt.Fprintf(os.Stderr, "scheduler tests: setting %s: %v\n", env, err)
			os.Exit(1)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}

// TestWorkspacesStayInsideTheTestRoot pins the guard above: the directory
// reviewOne creates workspaces in, and the boot sweep deletes from, must be a
// temp dir, never the developer's own state.
func TestWorkspacesStayInsideTheTestRoot(t *testing.T) {
	dir := config.Config{}.ReviewWorkspaceDir()
	if !strings.HasPrefix(dir, os.TempDir()) {
		t.Errorf("workspace dir = %s, want it under the temp dir %s", dir, os.TempDir())
	}
}
