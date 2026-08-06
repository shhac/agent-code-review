package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/shhac/lib-agent-cli/xdg"

	"github.com/shhac/agent-code-review/internal/config"
)

// TestAuthorsCommands drives the real cobra wiring against an isolated config
// and store: set preserves metadata and the group, ls accepts repo and group
// filters, rm is case-insensitive on handles, and an undefined group is
// refused at write time rather than silently resolving to comment-only.
func TestAuthorsCommands(t *testing.T) {
	cleanup := xdg.SetConfigBaseForTest(t.TempDir())
	defer cleanup()

	storePath := filepath.Join(t.TempDir(), "queue.duckdb")
	if err := config.Write(config.Config{
		Store: config.StoreSettings{Path: storePath},
		Authors: config.AuthorSettings{
			Groups: map[string]config.Group{"core": {Review: config.ReviewApprove, Engine: "claude"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) error {
		root := newRootCmd("test")
		root.SetArgs(args)
		return root.Execute()
	}

	if err := run("authors", "set", "*", "Alice", "core", "--name", "Alice A", "--email", "alice@example.com", "--slack-id", "U123"); err != nil {
		t.Fatal(err)
	}
	if err := run("authors", "set", "o/r", "Bob", config.GroupCommenter); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"authors", "ls", "--repo", "o/r"},
		{"authors", "ls", "--group", "core"},
		{"authors", "groups"},
		{"authors", "who", "Alice", "--repo", "o/r"},
	} {
		if err := run(args...); err != nil {
			t.Fatalf("%v failed: %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"authors", "set", "not-a-repo", "Mallory", "core"},
		{"authors", "rm", "not-a-repo", "Mallory"},
		{"authors", "ls", "--repo", "not-a-repo"},
		{"authors", "who", "Alice"}, // a policy is resolved per repo
		{"authors", "set", "o/r", "Mallory", "no-such-group"},
	} {
		if err := run(args...); err == nil {
			t.Fatalf("%v must be rejected", args)
		}
	}

	s, err := openStore(config.Read())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	authors, err := s.ListAuthors(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 2 {
		t.Fatalf("authors = %+v, want wildcard Alice and repo-scoped Bob", authors)
	}
	if authors[0].GitHubHandle != "Alice" || authors[0].Group != "core" ||
		authors[0].Name != "Alice A" || authors[0].Email != "alice@example.com" || authors[0].SlackID != "U123" {
		t.Errorf("group or metadata was not preserved: %+v", authors[0])
	}

	if err := run("authors", "rm", "*", "alice"); err != nil {
		t.Fatal(err)
	}
	authors, err = s.ListAuthors(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 1 || authors[0].GitHubHandle != "Bob" {
		t.Fatalf("rm should remove Alice case-insensitively, got %+v", authors)
	}
}
