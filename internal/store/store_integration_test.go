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

// getQueued finds one queue row — the Store contract has no single-row getter.
func getQueued(t *testing.T, s Store, repo string, number int) (Candidate, bool) {
	t.Helper()
	cands, err := s.ListQueue(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		if c.Number == number {
			return c, true
		}
	}
	return Candidate{}, false
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

// TestQueueLifecycle drives one candidate through the whole flow: enqueue,
// metadata refresh, claim, complete — asserting the queue/history invariants
// at each step.
func TestQueueLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 7, Type: TypeNew, URL: "u", HeadSHA: "sha1", DiscoveredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// Re-enqueue refreshes metadata but must not duplicate (PK) nor touch
	// claim/queue_pos.
	if err := s.SetQueuePos(ctx, "o/r", 7, -1); err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 7, Type: TypeNew, Title: "Real Title", Author: "alice", URL: "u", HeadSHA: "sha1"}); err != nil {
		t.Fatal(err)
	}
	queue, err := s.ListQueue(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 1 {
		t.Fatalf("re-enqueue duplicated the row: %d rows", len(queue))
	}
	c := queue[0]
	if c.Title != "Real Title" || c.QueuePos != -1 || c.ClaimedAt != nil {
		t.Errorf("enqueue upsert wrong: %+v", c)
	}

	// Claim marks it in-flight.
	claimAt := time.Now().UTC().Truncate(time.Second)
	if err := s.Claim(ctx, "o/r", 7, claimAt); err != nil {
		t.Fatal(err)
	}
	c, ok := getQueued(t, s, "o/r", 7)
	if !ok || c.ClaimedAt == nil {
		t.Fatalf("claim not visible: %+v", c)
	}

	// Complete removes the row and records history — atomically.
	if err := s.Complete(ctx, Review{Repo: "o/r", Number: 7, HeadSHA: "sha1", Verdict: "APPROVED", Engine: "test", ReviewedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, ok := getQueued(t, s, "o/r", 7); ok {
		t.Error("completed candidate must leave the queue")
	}
	last, ok, err := s.LastOutcome(ctx, "o/r", 7)
	if err != nil || !ok {
		t.Fatalf("history row missing after Complete: ok=%v err=%v", ok, err)
	}
	if last.Verdict != "APPROVED" || last.HeadSHA != "sha1" {
		t.Errorf("history row wrong: %+v", last)
	}

	// Re-enqueue after completion (new commits): plain insert, new SHA.
	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 7, Type: TypeRefreshed, HeadSHA: "sha2"}); err != nil {
		t.Fatal(err)
	}
	if c, ok := getQueued(t, s, "o/r", 7); !ok || c.HeadSHA != "sha2" || c.ClaimedAt != nil {
		t.Errorf("re-enqueue after completion wrong: ok=%v %+v", ok, c)
	}
}

// TestCompleteSHAGate pins the mid-review race: if the row's head advanced
// while the engine ran, Complete must record history but keep the (newer)
// row, clearing its claim so the next cycle picks it up.
func TestCompleteSHAGate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 8, Type: TypeNew, HeadSHA: "sha1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Claim(ctx, "o/r", 8, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Discovery updates the head mid-review.
	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 8, Type: TypeRefreshed, HeadSHA: "sha2"}); err != nil {
		t.Fatal(err)
	}
	// Engine finishes reviewing sha1.
	if err := s.Complete(ctx, Review{Repo: "o/r", Number: 8, HeadSHA: "sha1", Verdict: "COMMENTED", ReviewedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	c, ok := getQueued(t, s, "o/r", 8)
	if !ok {
		t.Fatal("row with newer SHA must survive Complete")
	}
	if c.HeadSHA != "sha2" || c.ClaimedAt != nil {
		t.Errorf("surviving row must carry sha2 unclaimed, got %+v", c)
	}
	if last, ok, _ := s.LastOutcome(ctx, "o/r", 8); !ok || last.HeadSHA != "sha1" {
		t.Errorf("history must still record the sha1 outcome, got ok=%v %+v", ok, last)
	}
}

// TestLastReviewVsLastOutcome pins the two history reads: LastReview sees only
// real verdicts (Refreshed detection), LastOutcome sees everything (same-SHA
// suppression).
func TestLastReviewVsLastOutcome(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	base := time.Now().Add(-time.Hour)
	steps := []Review{
		{Repo: "o/r", Number: 9, HeadSHA: "sha1", Verdict: "COMMENTED", ReviewedAt: base},
		{Repo: "o/r", Number: 9, HeadSHA: "sha2", Verdict: "SKIPPED", ReviewedAt: base.Add(time.Minute)},
		{Repo: "o/r", Number: 9, HeadSHA: "sha3", Verdict: "ERROR", ReviewedAt: base.Add(2 * time.Minute)},
	}
	for _, r := range steps {
		if err := s.Enqueue(ctx, Candidate{Repo: r.Repo, Number: r.Number, Type: TypeNew, HeadSHA: r.HeadSHA}); err != nil {
			t.Fatal(err)
		}
		if err := s.Complete(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	real, ok, err := s.LastReview(ctx, "o/r", 9)
	if err != nil || !ok {
		t.Fatalf("LastReview: ok=%v err=%v", ok, err)
	}
	if real.Verdict != "COMMENTED" || real.HeadSHA != "sha1" {
		t.Errorf("LastReview must skip SKIPPED/ERROR rows, got %+v", real)
	}
	outcome, ok, err := s.LastOutcome(ctx, "o/r", 9)
	if err != nil || !ok {
		t.Fatalf("LastOutcome: ok=%v err=%v", ok, err)
	}
	if outcome.Verdict != "ERROR" || outcome.HeadSHA != "sha3" {
		t.Errorf("LastOutcome must see every verdict, got %+v", outcome)
	}
	// Duplicates per PR are the point of history: all three rows exist.
	all, err := s.ListReviews(ctx, 10)
	if err != nil || len(all) != 3 {
		t.Errorf("history must keep duplicates: %d rows err=%v", len(all), err)
	}
}

// TestListQueueOrderingAndClaimVisibility pins the two contracts ListQueue's
// consumers rely on: the scheduler-order ORDER BY (manual positions first,
// then New before Refreshed, then lowest number) and the fact that claimed
// rows are still returned — availableCandidates and viewQueue both filter
// claims themselves and would silently break if the driver hid them.
func TestListQueueOrderingAndClaimVisibility(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rows := []Candidate{
		{Repo: "o/r", Number: 30, Type: TypeRefreshed},
		{Repo: "o/r", Number: 20, Type: TypeNew},
		{Repo: "o/r", Number: 10, Type: TypeNew},
		{Repo: "o/r", Number: 40, Type: TypeRefreshed},
	}
	for _, c := range rows {
		if err := s.Enqueue(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	// Manual position floats #40 to the very top, across types.
	if err := s.SetQueuePos(ctx, "o/r", 40, -1); err != nil {
		t.Fatal(err)
	}
	// A claimed row must remain visible.
	if err := s.Claim(ctx, "o/r", 10, time.Now()); err != nil {
		t.Fatal(err)
	}

	queue, err := s.ListQueue(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var order []int
	for _, c := range queue {
		order = append(order, c.Number)
	}
	want := []int{40, 10, 20, 30} // promoted, then new by number, then refreshed
	if len(order) != len(want) {
		t.Fatalf("queue = %v, want %v (claimed rows must not be hidden)", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("queue order = %v, want %v", order, want)
		}
	}
	for _, c := range queue {
		if c.Number == 10 && c.ClaimedAt == nil {
			t.Error("claimed row lost its claim in ListQueue")
		}
	}
}

// TestDequeueRecordsNothing distinguishes the "changed our mind" path from
// Complete: the row vanishes and history stays empty.
func TestDequeueRecordsNothing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 11, Type: TypeNew, HeadSHA: "sha1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Dequeue(ctx, "o/r", 11); err != nil {
		t.Fatal(err)
	}
	if _, ok := getQueued(t, s, "o/r", 11); ok {
		t.Error("dequeued row still present")
	}
	if _, ok, _ := s.LastOutcome(ctx, "o/r", 11); ok {
		t.Error("Dequeue must not write history")
	}
}

// TestAbsentRowEdges documents the advisory semantics when the queue row is
// gone (e.g. dequeued between ListQueue and reviewOne): Claim no-ops, and
// Complete still records the outcome (an orphan history row is harmless and
// preferable to losing a real review's record).
func TestAbsentRowEdges(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Claim(ctx, "o/r", 99, time.Now()); err != nil {
		t.Fatalf("Claim on missing row must no-op, got %v", err)
	}
	queue, err := s.ListQueue(ctx, "")
	if err != nil || len(queue) != 0 {
		t.Fatalf("Claim must not create rows: %v err=%v", queue, err)
	}

	if err := s.Complete(ctx, Review{Repo: "o/r", Number: 99, HeadSHA: "sha1", Verdict: "COMMENTED", ReviewedAt: time.Now()}); err != nil {
		t.Fatalf("Complete on missing row must not error, got %v", err)
	}
	if last, ok, _ := s.LastOutcome(ctx, "o/r", 99); !ok || last.Verdict != "COMMENTED" {
		t.Errorf("Complete must record history even without a queue row, got ok=%v %+v", ok, last)
	}
}

// TestHostileStringsRoundTrip drives GitHub-controlled strings through the
// real SQL path: quotes and injection shapes must store and read back intact.
func TestHostileStringsRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	title := `fix: O'Brien's "quote" bug'; DROP TABLE queue; --`
	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 9, Type: TypeNew, Title: title, Author: "o'malley"}); err != nil {
		t.Fatal(err)
	}
	c, ok := getQueued(t, s, "o/r", 9)
	if !ok {
		t.Fatal("candidate missing after hostile insert")
	}
	if c.Title != title || c.Author != "o'malley" {
		t.Errorf("hostile strings corrupted: title=%q author=%q", c.Title, c.Author)
	}
	// And the table is still there.
	if _, err := s.ListQueue(ctx, ""); err != nil {
		t.Errorf("queue table damaged: %v", err)
	}
}
