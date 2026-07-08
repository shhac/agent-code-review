package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/shhac/lib-agent-cli/xdg"

	"github.com/shhac/agent-code-review/internal/config"
)

// TestAuthorsCommands drives the real cobra wiring against an isolated config
// and store: allow preserves metadata, ls accepts a repo filter, and deny is
// case-insensitive on handles.
func TestAuthorsCommands(t *testing.T) {
	cleanup := xdg.SetConfigBaseForTest(t.TempDir())
	defer cleanup()

	storePath := filepath.Join(t.TempDir(), "queue.duckdb")
	if err := config.Write(config.Config{Store: config.StoreSettings{Path: storePath}}); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) error {
		root := newRootCmd("test")
		root.SetArgs(args)
		return root.Execute()
	}

	if err := run("authors", "allow", "*", "Alice", "--name", "Alice A", "--email", "alice@example.com", "--slack-id", "U123"); err != nil {
		t.Fatal(err)
	}
	if err := run("authors", "allow", "o/r", "Bob"); err != nil {
		t.Fatal(err)
	}
	if err := run("authors", "ls", "--repo", "o/r"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"authors", "allow", "not-a-repo", "Mallory"},
		{"authors", "deny", "not-a-repo", "Mallory"},
		{"authors", "ls", "--repo", "not-a-repo"},
	} {
		if err := run(args...); err == nil {
			t.Fatalf("%v must reject invalid repo scope", args)
		}
	}

	s, err := openStore(config.Read())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	authors, err := s.ListAllowedAuthors(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 2 {
		t.Fatalf("authors = %+v, want wildcard Alice and repo-scoped Bob", authors)
	}
	if authors[0].GitHubHandle != "Alice" || authors[0].Name != "Alice A" || authors[0].Email != "alice@example.com" || authors[0].SlackID != "U123" {
		t.Errorf("metadata was not preserved: %+v", authors[0])
	}

	if err := run("authors", "deny", "*", "alice"); err != nil {
		t.Fatal(err)
	}
	authors, err = s.ListAllowedAuthors(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 1 || authors[0].GitHubHandle != "Bob" {
		t.Fatalf("deny should remove Alice case-insensitively, got %+v", authors)
	}
}
