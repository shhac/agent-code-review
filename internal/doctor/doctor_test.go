package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
)

// Blocking is what the exit code and the boot warning both key off, so it
// must report only genuine blockers.
func TestBlockingSelectsFailedBlockers(t *testing.T) {
	got := Blocking([]Check{
		{Name: "gh", OK: true, Blocking: true},
		{Name: "engine:codex", OK: false, Blocking: true, Detail: "not on PATH"},
		{Name: "cosmetic", OK: false, Blocking: false},
	})
	if len(got) != 1 || got[0].Name != "engine:codex" {
		t.Errorf("Blocking() = %+v, want only the failed blocking check", got)
	}
}

func TestBlockingEmptyWhenHealthy(t *testing.T) {
	if got := Blocking([]Check{{Name: "gh", OK: true, Blocking: true}}); len(got) != 0 {
		t.Errorf("Blocking() = %+v, want none", got)
	}
}

// A missing binary must be diagnosed rather than reported as a version, and
// must carry a hint: the whole point is telling the operator what to do.
func TestBinaryCheckOnMissingBinary(t *testing.T) {
	c := binaryCheck(t.Context(), "engine:nope", "definitely-not-a-real-binary-xyz", "--version", "install it")
	if c.OK || !c.Blocking || c.Hint == "" {
		t.Errorf("check = %+v, want a blocking failure with a hint", c)
	}
}

// fakeClaude writes a stand-in binary that echoes canned `auth status --json`
// output, mirroring fakeCodex in internal/usage. exit lets a case simulate the
// CLI failing outright rather than reporting a logged-out state.
func fakeClaude(t *testing.T, body string, exit int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' '%s'\nexit %d\n", body, exit)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// claudeAuthCheck branches on CLI-reported JSON rather than an exit code,
// because `claude auth status` exits 0 while logged out. A wrong branch here
// either green-lights a broken engine at boot or blocks a healthy one.
func TestClaudeAuthCheck(t *testing.T) {
	for name, tc := range map[string]struct {
		body    string
		exit    int
		wantOK  bool
		wantSub string
	}{
		"logged in":     {`{"loggedIn":true,"authMethod":"claude.ai","subscriptionType":"max"}`, 0, true, "claude.ai max"},
		"logged out":    {`{"loggedIn":false}`, 0, false, "not logged in"},
		"malformed":     {`not json`, 0, false, "not readable JSON"},
		"command fails": {``, 3, false, "auth status failed"},
	} {
		c := claudeAuthCheck(t.Context(), fakeClaude(t, tc.body, tc.exit))
		if c.OK != tc.wantOK {
			t.Errorf("%s: OK = %v, want %v (detail %q)", name, c.OK, tc.wantOK, c.Detail)
		}
		if !strings.Contains(c.Detail, tc.wantSub) {
			t.Errorf("%s: detail = %q, want it to mention %q", name, c.Detail, tc.wantSub)
		}
		if !c.OK && c.Hint == "" {
			t.Errorf("%s: a failure must carry a hint", name)
		}
	}
}

// A binary that isn't there at all is the commonest failure, and must be
// reported as such rather than as an auth problem.
func TestClaudeAuthCheckMissingBinary(t *testing.T) {
	c := claudeAuthCheck(t.Context(), "definitely-not-a-real-binary-xyz")
	if c.OK || !strings.Contains(c.Detail, "not on PATH") {
		t.Errorf("check = %+v, want a not-on-PATH failure", c)
	}
}

// Check.Name is treated as a unique key: the CLI reports only the first
// failure, and consumers read the rows as one-per-check. Two config problems
// must therefore fold into one row rather than emitting two rows sharing a
// name, which silently hid the second problem.
func TestConfigCheckFoldsEveryProblemIntoOneRow(t *testing.T) {
	c := configCheck([]string{"model unsupported in auto mode", "allow_tools bypasses the classifier"})
	if c.OK || c.Name != "engine-config" {
		t.Fatalf("check = %+v, want a single failing engine-config row", c)
	}
	for _, want := range []string{"model unsupported", "bypasses the classifier"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("detail = %q, must mention %q — a hidden problem is the bug this fixes", c.Detail, want)
		}
	}
	if c.Hint == "" {
		t.Error("a failing check must carry a hint")
	}
}

func TestConfigCheckPassesWhenNothingConflicts(t *testing.T) {
	if c := configCheck(nil); !c.OK || c.Name != "engine-config" {
		t.Errorf("check = %+v, want a passing engine-config row", c)
	}
}

// Whatever Run returns, no two checks may share a name.
func TestRunEmitsUniqueCheckNames(t *testing.T) {
	seen := map[string]int{}
	for _, c := range Run(t.Context(), config.Config{}) {
		seen[c.Name]++
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("check %q emitted %d times; Name is used as a unique key", name, n)
		}
	}
}
