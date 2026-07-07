//go:build integration

package store

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// newTestStore opens a fresh DuckDB store in a temp dir. Skips when the
// duckdb CLI isn't installed (CI runs unit tests only).
func newTestStore(t *testing.T) Store {
	t.Helper()
	if _, err := exec.LookPath("duckdb"); err != nil {
		t.Skip("duckdb CLI not on PATH")
	}
	s, err := Open("duckdb", filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestIsAuthorAllowed covers the store half of the approval gate — the single
// query that decides whether a PR may be APPROVED at all.
func TestIsAuthorAllowed(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.AllowAuthor(ctx, AllowedAuthor{Repo: "org/repo-a", GitHubHandle: "Alice"}))
	must(s.AllowAuthor(ctx, AllowedAuthor{Repo: WildcardRepo, GitHubHandle: "bob"}))

	cases := []struct {
		name, repo, handle string
		want               bool
	}{
		{"exact repo match", "org/repo-a", "Alice", true},
		{"case-insensitive handle", "org/repo-a", "alice", true},
		{"listed repo does not leak to another repo", "org/repo-b", "alice", false},
		{"wildcard applies everywhere", "org/repo-b", "bob", true},
		{"wildcard case-insensitive", "org/repo-a", "BOB", true},
		{"unknown author", "org/repo-a", "mallory", false},
		{"empty handle never allowed", "org/repo-a", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.IsAuthorAllowed(ctx, tc.repo, tc.handle)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("IsAuthorAllowed(%q, %q) = %v, want %v", tc.repo, tc.handle, got, tc.want)
			}
		})
	}

	// Deny closes the gate again.
	must(s.DenyAuthor(ctx, WildcardRepo, "BOB")) // case-insensitive delete
	if got, _ := s.IsAuthorAllowed(ctx, "org/repo-b", "bob"); got {
		t.Error("bob should be denied after DenyAuthor")
	}

	// AllowAuthor upserts metadata without duplicating rows.
	must(s.AllowAuthor(ctx, AllowedAuthor{Repo: "org/repo-a", GitHubHandle: "Alice", Name: "Alice A"}))
	authors, err := s.ListAllowedAuthors(ctx, "org/repo-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 1 || authors[0].Name != "Alice A" {
		t.Errorf("expected single upserted row with updated name, got %+v", authors)
	}
}

// TestRequeuePreservesMetadata pins the manual-add primitive: new rows insert
// queued; existing rows keep discovered metadata and only flip status.
func TestRequeuePreservesMetadata(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Requeue(ctx, Candidate{Repo: "o/r", Number: 7, Type: TypeNew, URL: "u", DiscoveredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// Discovery enriches it, then it gets reviewed.
	if err := s.UpsertCandidate(ctx, Candidate{Repo: "o/r", Number: 7, Type: TypeNew, Title: "Real Title", Author: "alice", URL: "u", HeadSHA: "sha1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(ctx, "o/r", 7, StatusReviewed); err != nil {
		t.Fatal(err)
	}
	// Manual re-add: back to queued, metadata intact.
	if err := s.Requeue(ctx, Candidate{Repo: "o/r", Number: 7, Type: TypeNew}); err != nil {
		t.Fatal(err)
	}
	c, ok, err := s.GetCandidate(ctx, "o/r", 7)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if c.Status != StatusQueued || c.Title != "Real Title" || c.Author != "alice" {
		t.Errorf("requeue lost state: %+v", c)
	}
	// Discovery's upsert must never touch status.
	if err := s.SetStatus(ctx, "o/r", 7, StatusReviewing); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCandidate(ctx, Candidate{Repo: "o/r", Number: 7, Type: TypeNew, Title: "Real Title", Author: "alice", HeadSHA: "sha2"}); err != nil {
		t.Fatal(err)
	}
	c, _, _ = s.GetCandidate(ctx, "o/r", 7)
	if c.Status != StatusReviewing {
		t.Errorf("discovery upsert changed status to %s — must preserve", c.Status)
	}
}

// TestHostileStringsRoundTrip drives GitHub-controlled strings through the
// real SQL path: quotes and injection shapes must store and read back intact.
func TestHostileStringsRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	title := `fix: O'Brien's "quote" bug'; DROP TABLE candidates; --`
	if err := s.UpsertCandidate(ctx, Candidate{Repo: "o/r", Number: 9, Type: TypeNew, Title: title, Author: "o'malley"}); err != nil {
		t.Fatal(err)
	}
	c, ok, err := s.GetCandidate(ctx, "o/r", 9)
	if err != nil || !ok {
		t.Fatalf("get after hostile insert: ok=%v err=%v", ok, err)
	}
	if c.Title != title || c.Author != "o'malley" {
		t.Errorf("hostile strings corrupted: title=%q author=%q", c.Title, c.Author)
	}
	// And the table is still there.
	if _, err := s.ListCandidates(ctx, Filter{}); err != nil {
		t.Errorf("candidates table damaged: %v", err)
	}
}
