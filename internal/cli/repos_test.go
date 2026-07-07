package cli

import (
	"testing"

	"github.com/shhac/lib-agent-cli/xdg"

	"github.com/shhac/agent-code-review/internal/config"
)

func TestRemoveFold(t *testing.T) {
	got := removeFold([]string{"a/b", "C/D", "e/f"}, "c/d")
	if len(got) != 2 || got[0] != "a/b" || got[1] != "e/f" {
		t.Errorf("case-insensitive removal failed: %v", got)
	}
	if got := removeFold([]string{"a/b"}, "x/y"); len(got) != 1 {
		t.Errorf("no-match must keep the list: %v", got)
	}
	if got := removeFold(nil, "a/b"); len(got) != 0 {
		t.Errorf("nil list: %v", got)
	}
}

// TestReposScopeReconciliation drives the real commands against an isolated
// config dir: add scoped → re-add unscoped flips it off → rm clears both lists.
func TestReposScopeReconciliation(t *testing.T) {
	cleanup := xdg.SetConfigBaseForTest(t.TempDir())
	defer cleanup()

	run := func(args ...string) error {
		root := newRootCmd("test")
		root.SetArgs(args)
		return root.Execute()
	}

	if err := run("repos", "add", "o/scoped", "--allowed-authors-only"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Read()
	if !cfg.WatchesRepo("o/scoped") || !cfg.AuthorScopedRepo("o/scoped") {
		t.Fatalf("scoped add failed: %+v", cfg)
	}

	// Re-adding without the flag flips the scope off but keeps it watched.
	if err := run("repos", "add", "o/scoped"); err != nil {
		t.Fatal(err)
	}
	cfg = config.Read()
	if !cfg.WatchesRepo("o/scoped") || cfg.AuthorScopedRepo("o/scoped") {
		t.Fatalf("scope flip-off failed: %+v", cfg)
	}

	// Scope it again, then rm must clear both memberships.
	if err := run("repos", "add", "o/scoped", "--allowed-authors-only"); err != nil {
		t.Fatal(err)
	}
	if err := run("repos", "rm", "o/scoped"); err != nil {
		t.Fatal(err)
	}
	cfg = config.Read()
	if cfg.WatchesRepo("o/scoped") || cfg.AuthorScopedRepo("o/scoped") {
		t.Fatalf("rm must clear watch + scope: %+v", cfg)
	}
}
