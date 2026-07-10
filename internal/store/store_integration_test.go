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

// mustClaim claims a row with a fresh test lease, failing the test if the
// compare-and-swap loses — for tests where the claim is setup, not the
// subject.
func mustClaim(t *testing.T, s Store, repo string, number int, at time.Time, workDir string) {
	t.Helper()
	ok, err := s.Claim(context.Background(), repo, number, Lease{
		At: at, WorkDir: workDir, Host: "test-host", PID: 4242, StaleAfter: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("claim of %s#%d unexpectedly lost", repo, number)
	}
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

// TestReadOnlyStoreReadsButRefusesWrites covers the inspect-only store used by
// `serve --read-only`: it attaches to an existing DB without applying the schema,
// can read, and lets DuckDB refuse any write.
func TestReadOnlyStoreReadsButRefusesWrites(t *testing.T) {
	if _, err := exec.LookPath("duckdb"); err != nil {
		t.Skip("duckdb CLI not on PATH")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ro.duckdb")

	// Seed one queue row through a normal read-write store.
	rw, err := Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := rw.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := rw.Enqueue(ctx, Candidate{Repo: "o/r", Number: 7, Type: TypeNew, URL: "u", HeadSHA: "sha1", DiscoveredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	_ = rw.Close()

	// A read-only store attaches (no schema write) and can read the seeded row.
	ro, err := OpenReadOnly("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ro.Close() })
	if err := ro.Init(ctx); err != nil {
		t.Fatalf("read-only Init should validate reachability, got %v", err)
	}
	if _, ok := getQueued(t, ro, "o/r", 7); !ok {
		t.Fatal("read-only store did not see the seeded row")
	}

	// Writes are refused by DuckDB itself — no per-method guard needed.
	if err := ro.Enqueue(ctx, Candidate{Repo: "o/r", Number: 8, Type: TypeNew}); err == nil {
		t.Fatal("read-only store accepted a write")
	}
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
	if err := s.Reorder(ctx, []QueuePosition{{Repo: "o/r", Number: 7, Position: -1}}); err != nil {
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
	mustClaim(t, s, "o/r", 7, claimAt, "/tmp/example-workdir-7")
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

func TestReorderAppliesEveryPositionTogether(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, number := range []int{1, 2, 3} {
		if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: number, Type: TypeNew, DiscoveredAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Reorder(ctx, []QueuePosition{
		{Repo: "o/r", Number: 3, Position: 1},
		{Repo: "o/r", Number: 1, Position: 2},
		{Repo: "o/r", Number: 2, Position: 3},
	}); err != nil {
		t.Fatal(err)
	}
	queue, err := s.ListQueue(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	got := []int{queue[0].Number, queue[1].Number, queue[2].Number}
	if got[0] != 3 || got[1] != 1 || got[2] != 2 {
		t.Errorf("order = %v, want [3 1 2]", got)
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
	mustClaim(t, s, "o/r", 8, time.Now(), "/tmp/example-workdir-8")
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

	sweep := func(h int) time.Time { return time.Date(2026, 7, 1, h, 0, 0, 0, time.UTC) }
	rows := []Candidate{
		// #30 was discovered hours before the others: FIFO puts it first even
		// though it is Refreshed and the later sweep found New PRs.
		{Repo: "o/r", Number: 30, Type: TypeRefreshed, DiscoveredAt: sweep(9)},
		{Repo: "o/r", Number: 20, Type: TypeNew, DiscoveredAt: sweep(12)},
		{Repo: "o/r", Number: 10, Type: TypeNew, DiscoveredAt: sweep(12)},
		{Repo: "o/r", Number: 40, Type: TypeRefreshed, DiscoveredAt: sweep(12)},
	}
	for _, c := range rows {
		if err := s.Enqueue(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	// Manual position floats #40 to the very top, across everything.
	if err := s.Reorder(ctx, []QueuePosition{{Repo: "o/r", Number: 40, Position: -1}}); err != nil {
		t.Fatal(err)
	}
	// A claimed row must remain visible.
	mustClaim(t, s, "o/r", 10, time.Now(), "/tmp/example-workdir-10")

	queue, err := s.ListQueue(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var order []int
	for _, c := range queue {
		order = append(order, c.Number)
	}
	want := []int{40, 30, 10, 20} // promoted, then FIFO by discovery, then new-before-refreshed/number within a sweep
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

// TestAbsentRowEdges documents the semantics when the queue row is gone
// (e.g. dequeued between ListQueue and reviewOne): Claim loses the CAS, and
// Complete still records the outcome (an orphan history row is harmless and
// preferable to losing a real review's record).
func TestAbsentRowEdges(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ok, err := s.Claim(ctx, "o/r", 99, Lease{At: time.Now(), WorkDir: "/tmp/example-workdir-99", Host: "h", PID: 1, StaleAfter: time.Hour})
	if err != nil || ok {
		t.Fatalf("Claim on missing row must lose cleanly, got ok=%v err=%v", ok, err)
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

// TestCompleteSnapshotRoundTrip pins the display snapshot: Title and Author
// written by Complete must read back intact through history (guards the
// positional INSERT's column/value alignment), including hostile strings
// through the q() escaping on the new columns.
func TestCompleteSnapshotRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	title := `feat: O'Neill's "quoted" title`
	c := Candidate{Repo: "o/r", Number: 21, Type: TypeNew, Title: title, Author: "o'connor", HeadSHA: "sha1"}
	if err := s.Enqueue(ctx, c); err != nil {
		t.Fatal(err)
	}
	mustClaim(t, s, "o/r", 21, time.Now(), "/tmp/example-workdir-21")
	rec := ReviewFrom(c, "COMMENTED", "test-engine", time.Now().Add(-90*time.Second))
	rec.WorkDir = "/tmp/example-workdir-21"
	rec.TokensUsed = 192575
	rec.Model = "gpt-5.6-terra"
	rec.Effort = "high"
	rec.CodexVersion = "Codex CLI 0.144.0"
	if err := s.Complete(ctx, rec); err != nil {
		t.Fatal(err)
	}
	last, ok, err := s.LastOutcome(ctx, "o/r", 21)
	if err != nil || !ok {
		t.Fatalf("history row missing: ok=%v err=%v", ok, err)
	}
	if last.Title != title || last.Author != "o'connor" {
		t.Errorf("snapshot corrupted: title=%q author=%q", last.Title, last.Author)
	}
	if last.Verdict != "COMMENTED" || last.Engine != "test-engine" || last.HeadSHA != "sha1" {
		t.Errorf("columns misaligned: %+v", last)
	}
	if last.DurationSecs < 89 || last.DurationSecs > 95 {
		t.Errorf("duration_secs = %d, want ~90", last.DurationSecs)
	}
	if last.WorkDir != "/tmp/example-workdir-21" {
		t.Errorf("work_dir = %q, want the claimed workspace", last.WorkDir)
	}
	if last.TokensUsed != 192575 {
		t.Errorf("tokens_used = %d, want 192575", last.TokensUsed)
	}
	if last.Model != "gpt-5.6-terra" || last.Effort != "high" {
		t.Errorf("model/effort = %q/%q, want gpt-5.6-terra/high", last.Model, last.Effort)
	}
	if last.CodexVersion != "Codex CLI 0.144.0" {
		t.Errorf("codex_version = %q", last.CodexVersion)
	}
	all, err := s.ListReviews(ctx, 5)
	if err != nil || len(all) != 1 || all[0].Title != title {
		t.Errorf("ListReviews must carry the snapshot too: %+v err=%v", all, err)
	}
	if total, err := s.TokensUsed(ctx, time.Time{}); err != nil || total != 192575 {
		t.Errorf("TokensUsed(all time) = %d err=%v, want 192575", total, err)
	}
	if recent, err := s.TokensUsed(ctx, time.Now().Add(-time.Hour)); err != nil || recent != 192575 {
		t.Errorf("TokensUsed(last hour) = %d err=%v, want 192575", recent, err)
	}
	if none, err := s.TokensUsed(ctx, time.Now().Add(time.Hour)); err != nil || none != 0 {
		t.Errorf("TokensUsed(future window) = %d err=%v, want 0", none, err)
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

// TestEnqueueSourceEscalation: source only ever escalates to manual. A manual
// add wins over a later discovery sweep (the precheck bypass the user asked
// for must survive), while a discovered row a human re-adds becomes manual.
func TestEnqueueSourceEscalation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 12, Type: TypeNew, Source: SourceDiscovered}); err != nil {
		t.Fatal(err)
	}
	if c, _ := getQueued(t, s, "o/r", 12); c.Source != SourceDiscovered {
		t.Fatalf("fresh discovered row source = %q", c.Source)
	}
	// Human re-adds it: escalate.
	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 12, Type: TypeNew, Source: SourceManual}); err != nil {
		t.Fatal(err)
	}
	if c, _ := getQueued(t, s, "o/r", 12); c.Source != SourceManual {
		t.Fatalf("manual re-add must escalate source, got %q", c.Source)
	}
	// Discovery sweeps again: must NOT downgrade.
	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 12, Type: TypeRefreshed, Source: SourceDiscovered}); err != nil {
		t.Fatal(err)
	}
	if c, _ := getQueued(t, s, "o/r", 12); c.Source != SourceManual {
		t.Fatalf("discovery must not downgrade a manual row, got %q", c.Source)
	}
	// Empty source defaults to discovered.
	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 13, Type: TypeNew}); err != nil {
		t.Fatal(err)
	}
	if c, _ := getQueued(t, s, "o/r", 13); c.Source != SourceDiscovered {
		t.Fatalf("empty source must default to discovered, got %q", c.Source)
	}
}

// TestListAllowedAuthorsAlphabetical: the list is about authors, so it comes
// back alphabetical by handle, case-insensitively (DuckDB's raw TEXT order
// would put "Zed" before "alice"), with repo as the tiebreak.
func TestListAllowedAuthorsAlphabetical(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, a := range []AllowedAuthor{
		{Repo: "org/b", GitHubHandle: "Zed"},
		{Repo: "org/a", GitHubHandle: "alice"},
		{Repo: WildcardRepo, GitHubHandle: "Bob"},
		{Repo: "org/a", GitHubHandle: "Bob"},
	} {
		if err := s.AllowAuthor(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	authors, err := s.ListAllowedAuthors(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range authors {
		got = append(got, a.GitHubHandle+"@"+a.Repo)
	}
	want := []string{"alice@org/a", "Bob@*", "Bob@org/a", "Zed@org/b"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("authors order = %v, want %v", got, want)
		}
	}
}

// TestEnqueueDiscoveredAtFirstSeen: a sweep re-seeing pending work is not a
// new discovery — discovered_at must keep its first-seen value, not track the
// latest sweep.
func TestEnqueueDiscoveredAtFirstSeen(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first := time.Now().UTC().Truncate(time.Second).Add(-3 * time.Hour)
	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 20, Type: TypeNew, DiscoveredAt: first}); err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 20, Type: TypeNew, DiscoveredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	c, _ := getQueued(t, s, "o/r", 20)
	if !c.DiscoveredAt.Equal(first) {
		t.Errorf("discovered_at bumped by re-enqueue: got %v, want %v", c.DiscoveredAt, first)
	}
}

// TestEnqueueHoldSemantics pins the eligibility-hold upsert rules: a hold
// only ever extends (later wins, earlier is ignored), a manual enqueue clears
// it, and discovery never re-imposes one on a manual row.
func TestEnqueueHoldSemantics(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	at := func(d time.Duration) *time.Time { t := base.Add(d); return &t }

	enq := func(eligible *time.Time, reason, source string) {
		t.Helper()
		if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 21, Type: TypeNew, EligibleAt: eligible, HoldReason: reason, Source: source}); err != nil {
			t.Fatal(err)
		}
	}
	hold := func() (*time.Time, string) {
		t.Helper()
		c, ok := getQueued(t, s, "o/r", 21)
		if !ok {
			t.Fatal("row missing")
		}
		return c.EligibleAt, c.HoldReason
	}

	// Fresh row with a settling hold.
	enq(at(15*time.Minute), HoldSettling, SourceDiscovered)
	if e, r := hold(); e == nil || !e.Equal(*at(15 * time.Minute)) || r != HoldSettling {
		t.Fatalf("fresh hold not recorded: eligible=%v reason=%q", e, r)
	}
	// A later hold extends (and its reason wins).
	enq(at(90*time.Minute), HoldCooldown, SourceDiscovered)
	if e, r := hold(); e == nil || !e.Equal(*at(90 * time.Minute)) || r != HoldCooldown {
		t.Fatalf("later hold must extend: eligible=%v reason=%q", e, r)
	}
	// An earlier hold must not shrink it.
	enq(at(5*time.Minute), HoldSettling, SourceDiscovered)
	if e, r := hold(); e == nil || !e.Equal(*at(90 * time.Minute)) || r != HoldCooldown {
		t.Fatalf("earlier hold must not shrink: eligible=%v reason=%q", e, r)
	}
	// A hold-free sweep must not clear an existing hold either.
	enq(nil, "", SourceDiscovered)
	if e, _ := hold(); e == nil || !e.Equal(*at(90 * time.Minute)) {
		t.Fatalf("hold-free sweep must keep the hold: eligible=%v", e)
	}
	// A manual enqueue clears the hold.
	enq(nil, "", SourceManual)
	if e, r := hold(); e != nil || r != "" {
		t.Fatalf("manual enqueue must clear the hold: eligible=%v reason=%q", e, r)
	}
	// Discovery must never re-impose a hold on a manual row.
	enq(at(2*time.Hour), HoldCooldown, SourceDiscovered)
	if e, r := hold(); e != nil || r != "" {
		t.Fatalf("discovery must not hold a manual row: eligible=%v reason=%q", e, r)
	}
}

// TestClaimCAS pins the compare-and-swap lease: a live claim cannot be
// stolen, a stale one can, host+pid are recorded for reconciliation, and
// ClearClaim releases everything.
func TestClaimCAS(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 30, Type: TypeNew, HeadSHA: "sha1"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	lease := func(at time.Time, pid int) Lease {
		return Lease{At: at, WorkDir: "/tmp/example-workdir-30", Host: "host-a", PID: pid, StaleAfter: 2 * time.Hour}
	}

	// First claim wins and records its identity.
	if ok, err := s.Claim(ctx, "o/r", 30, lease(now, 100)); err != nil || !ok {
		t.Fatalf("first claim must win: ok=%v err=%v", ok, err)
	}
	c, _ := getQueued(t, s, "o/r", 30)
	if c.ClaimHost != "host-a" || c.ClaimPID != 100 {
		t.Errorf("claim identity not recorded: %+v", c)
	}

	// A second claimant loses while the lease is live — and must not clobber
	// the holder's identity.
	if ok, err := s.Claim(ctx, "o/r", 30, lease(now.Add(time.Minute), 200)); err != nil || ok {
		t.Fatalf("live lease must not be stolen: ok=%v err=%v", ok, err)
	}
	if c, _ := getQueued(t, s, "o/r", 30); c.ClaimPID != 100 {
		t.Errorf("losing claim overwrote the holder: %+v", c)
	}

	// Once stale (older than StaleAfter), the claim is reclaimable.
	if ok, err := s.Claim(ctx, "o/r", 30, lease(now.Add(3*time.Hour), 200)); err != nil || !ok {
		t.Fatalf("stale lease must be reclaimable: ok=%v err=%v", ok, err)
	}
	if c, _ := getQueued(t, s, "o/r", 30); c.ClaimPID != 200 {
		t.Errorf("reclaim must record the new holder: %+v", c)
	}

	// ClearClaim releases the row entirely.
	if err := s.ClearClaim(ctx, "o/r", 30); err != nil {
		t.Fatal(err)
	}
	if c, _ := getQueued(t, s, "o/r", 30); c.ClaimedAt != nil || c.ClaimHost != "" || c.ClaimPID != 0 {
		t.Errorf("ClearClaim must clear the whole lease: %+v", c)
	}
}

// TestRunningRuns: only status='running' rows surface — the reconciliation
// input must not include finished runs.
func TestRunningRuns(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.StartRun(ctx, Run{ID: "r1", StartedAt: time.Now().Add(-time.Hour), Host: "h", PID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.StartRun(ctx, Run{ID: "r2", StartedAt: time.Now(), Host: "h", PID: 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, "r1", "done"); err != nil {
		t.Fatal(err)
	}
	runs, err := s.RunningRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != "r2" {
		t.Errorf("RunningRuns = %+v, want just r2", runs)
	}
}

// TestPromote: promote floats the row, clears the hold, and escalates source
// to manual — the one-write "review this now" action.
func TestPromote(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	eligible := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 22, Type: TypeNew, EligibleAt: &eligible, HoldReason: HoldCooldown}); err != nil {
		t.Fatal(err)
	}
	if err := s.Promote(ctx, "o/r", 22); err != nil {
		t.Fatal(err)
	}
	c, ok := getQueued(t, s, "o/r", 22)
	if !ok {
		t.Fatal("row missing after promote")
	}
	if c.QueuePos != -1 || c.EligibleAt != nil || c.HoldReason != "" || c.Source != SourceManual {
		t.Errorf("promote must float, clear hold, and escalate: %+v", c)
	}
}
