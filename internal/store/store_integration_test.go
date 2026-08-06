//go:build integration

package store

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
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
// compare-and-swap loses: for tests where the claim is setup, not the
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

// getQueued finds one queue row; the Store contract has no single-row getter.
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

	// Writes are refused by DuckDB itself; no per-method guard needed.
	if err := ro.Enqueue(ctx, Candidate{Repo: "o/r", Number: 8, Type: TypeNew, HeadSHA: "sha"}); err == nil {
		t.Fatal("read-only store accepted a write")
	}
}

// TestAuthorGroup covers the store half of the policy cascade: which roster
// row applies to an author on a repo. The precedence lives in the query, so
// this is where "exact repo beats wildcard" is pinned.
func TestAuthorGroup(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.SetAuthorGroup(ctx, Author{Repo: "org/repo-a", GitHubHandle: "Alice", Group: "core"}))
	must(s.SetAuthorGroup(ctx, Author{Repo: WildcardRepo, GitHubHandle: "bob", Group: "outsider"}))
	// Bob is an outsider everywhere EXCEPT repo-a, where a specific row wins.
	must(s.SetAuthorGroup(ctx, Author{Repo: "org/repo-a", GitHubHandle: "bob", Group: "core"}))

	cases := []struct {
		name, repo, handle string
		want               config.Membership
	}{
		{"exact repo match", "org/repo-a", "Alice", config.Membership{Group: "core", Repo: "org/repo-a"}},
		{"case-insensitive handle", "org/repo-a", "alice", config.Membership{Group: "core", Repo: "org/repo-a"}},
		{"a repo row does not leak to another repo", "org/repo-b", "alice", config.Membership{}},
		{"wildcard applies everywhere else", "org/repo-b", "bob", config.Membership{Group: "outsider", Repo: WildcardRepo}},
		{"exact repo beats the wildcard row", "org/repo-a", "BOB", config.Membership{Group: "core", Repo: "org/repo-a"}},
		{"unknown author is unlisted", "org/repo-a", "mallory", config.Membership{}},
		{"empty handle is unlisted", "org/repo-a", "", config.Membership{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.AuthorGroup(ctx, tc.repo, tc.handle)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("AuthorGroup(%q, %q) = %+v, want %+v", tc.repo, tc.handle, got, tc.want)
			}
		})
	}

	// Removal drops the author back to unlisted.
	must(s.RemoveAuthor(ctx, WildcardRepo, "BOB")) // case-insensitive delete
	if got, _ := s.AuthorGroup(ctx, "org/repo-b", "bob"); got.Group != "" {
		t.Errorf("bob should be unlisted on repo-b after removal, got %+v", got)
	}

	// SetAuthorGroup upserts metadata and the group without duplicating rows.
	must(s.SetAuthorGroup(ctx, Author{Repo: "org/repo-a", GitHubHandle: "Alice", Group: "outsider", Name: "Alice A"}))
	authors, err := s.ListAuthors(ctx, "org/repo-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 2 {
		t.Fatalf("expected the two repo-a rows, got %+v", authors)
	}
	for _, a := range authors {
		if a.GitHubHandle == "Alice" && (a.Name != "Alice A" || a.Group != "outsider") {
			t.Errorf("upsert did not update the row: %+v", a)
		}
	}

	// Listing narrows by group as well as repo.
	core, err := s.ListAuthors(ctx, "", "core")
	if err != nil {
		t.Fatal(err)
	}
	if len(core) != 1 || core[0].GitHubHandle != "bob" {
		t.Errorf("group filter should leave only bob@org/repo-a, got %+v", core)
	}
}

// TestAuthorGroupBackfillsLegacyRows: rows written before group_name existed
// WERE the allow list, which meant exactly "this author may be approved". They
// must keep meaning that, so an upgraded store behaves identically.
func TestAuthorGroupBackfillsLegacyRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	d, ok := s.(*duckDB)
	if !ok {
		t.Skip("legacy backfill is a duckdb concern")
	}
	// A pre-groups row: every column except group_name.
	if err := d.exec(ctx, `INSERT INTO allowed_authors (repo, github_handle, name) VALUES ('org/repo-a', 'legacy', 'Legacy L')`); err != nil {
		t.Fatal(err)
	}
	if err := d.exec(ctx, `UPDATE allowed_authors SET group_name = NULL WHERE github_handle = 'legacy'`); err != nil {
		t.Fatal(err)
	}

	got, err := s.AuthorGroup(ctx, "org/repo-a", "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if got.Group != config.GroupApprover {
		t.Errorf("a null group_name must read as %q, got %q", config.GroupApprover, got.Group)
	}
	authors, err := s.ListAuthors(ctx, "org/repo-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 1 || authors[0].Group != config.GroupApprover {
		t.Errorf("listing must show the same backfilled group, got %+v", authors)
	}
}

// TestQueueLifecycle drives one candidate through the whole flow: enqueue,
// metadata refresh, claim, complete, asserting the queue/history invariants
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

	// Complete removes the row and records history, atomically.
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
		if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: number, Type: TypeNew, HeadSHA: "sha", DiscoveredAt: time.Now()}); err != nil {
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

	// NULL boundary: an empty SHA renders as NULL. history.head_sha is NOT
	// NULL, so such a row could never Complete (the transaction would abort
	// every cycle, leaving the row stuck). Enqueue must refuse it at the
	// entrance so the queue can't hold un-completable work.
	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 9, Type: TypeNew, HeadSHA: ""}); err == nil {
		t.Error("enqueueing an empty head SHA must be rejected: the row could never Complete")
	}
	if _, ok := getQueued(t, s, "o/r", 9); ok {
		t.Error("rejected empty-SHA candidate must not be enqueued")
	}
}

// TestListReviewsSince pins the dashboard feed's time filter: the boundary
// row (reviewed_at == since) is included, earlier rows are excluded, results
// come oldest-first, and a zero since means "no lower bound" (matching
// FreshTokens) rather than silently matching nothing via `>= NULL`.
func TestListReviewsSince(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	for i, at := range []time.Time{base.Add(-time.Hour), base, base.Add(time.Hour)} {
		rec := Review{Repo: "o/r", Number: 50 + i, HeadSHA: "sha", Verdict: "COMMENTED", ReviewedAt: at}
		if err := s.Complete(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.ListReviewsSince(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Number != 51 || got[1].Number != 52 {
		t.Fatalf("since filter must include the boundary row, oldest first; got %+v", got)
	}

	all, err := s.ListReviewsSince(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("zero since must mean no lower bound, got %d rows", len(all))
	}
}

// TestActiveRunStaleness pins the run-lock predicate: a fresh running row
// blocks cycles, a crashed row older than the window stops blocking them,
// and a finished row never blocks. A regression in the cutoff comparison
// would either allow overlapping cycles or wedge the scheduler for a whole
// lease window.
func TestActiveRunStaleness(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	staleAfter := 2 * time.Hour

	// A crashed run from 3h ago: beyond the window, must not block.
	if err := s.StartRun(ctx, Run{ID: "stale", StartedAt: time.Now().Add(-3 * time.Hour), Host: "h", PID: 1}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ActiveRun(ctx, staleAfter); err != nil || ok {
		t.Fatalf("a run older than the stale window must not be active, got ok=%v err=%v", ok, err)
	}

	// A fresh running row blocks, and is the one returned.
	if err := s.StartRun(ctx, Run{ID: "fresh", StartedAt: time.Now().Add(-time.Minute), Host: "h", PID: 2}); err != nil {
		t.Fatal(err)
	}
	run, ok, err := s.ActiveRun(ctx, staleAfter)
	if err != nil || !ok || run.ID != "fresh" {
		t.Fatalf("fresh running row must be active, got ok=%v id=%q err=%v", ok, run.ID, err)
	}

	// Finishing it releases the lock.
	if err := s.FinishRun(ctx, "fresh", "done"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.ActiveRun(ctx, staleAfter); ok {
		t.Error("a finished run must not be active")
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
// rows are still returned; availableCandidates and viewQueue both filter
// claims themselves and would silently break if the driver hid them.
func TestListQueueOrderingAndClaimVisibility(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sweep := func(h int) time.Time { return time.Date(2026, 7, 1, h, 0, 0, 0, time.UTC) }
	rows := []Candidate{
		// #30 was discovered hours before the others: FIFO puts it first even
		// though it is Refreshed and the later sweep found New PRs.
		{Repo: "o/r", Number: 30, Type: TypeRefreshed, HeadSHA: "sha", DiscoveredAt: sweep(9)},
		{Repo: "o/r", Number: 20, Type: TypeNew, HeadSHA: "sha", DiscoveredAt: sweep(12)},
		{Repo: "o/r", Number: 10, Type: TypeNew, HeadSHA: "sha", DiscoveredAt: sweep(12)},
		{Repo: "o/r", Number: 40, Type: TypeRefreshed, HeadSHA: "sha", DiscoveredAt: sweep(12)},
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
	rec.FreshTokens = 42575
	rec.InputTokens = 40000
	rec.OutputTokens = 2000
	rec.CacheWriteTokens = 575
	rec.CacheReadTokens = 150000
	rec.ReasoningTokens = 900
	rec.UsageRaw = `[{"input_tokens":40000,"service_tier":"standard"}]`
	rec.EstCostUSD = 0.4177
	rec.Model = "gpt-5.6-terra"
	rec.Effort = "high"
	rec.EngineVersion = "Codex CLI 0.144.0"
	rec.CostUSD = 0.6231
	if err := s.Complete(ctx, rec); err != nil {
		t.Fatal(err)
	}
	last, ok, err := s.LastOutcome(ctx, "o/r", 21)
	if err != nil || !ok {
		t.Fatalf("history row missing: ok=%v err=%v", ok, err)
	}
	// Distinct values on purpose: the INSERT is a positional %d list and these
	// three are all ints, so equal values would let a swap read back clean.
	if last.TokensUsed != 192575 || last.FreshTokens != 42575 || last.CacheReadTokens != 150000 {
		t.Errorf("token columns misaligned: total=%d fresh=%d cached=%d",
			last.TokensUsed, last.FreshTokens, last.CacheReadTokens)
	}
	if last.InputTokens != 40000 || last.OutputTokens != 2000 || last.CacheWriteTokens != 575 || last.ReasoningTokens != 900 {
		t.Errorf("per-class columns misaligned: in=%d out=%d write=%d reasoning=%d",
			last.InputTokens, last.OutputTokens, last.CacheWriteTokens, last.ReasoningTokens)
	}
	// The escape hatch is only an escape hatch if it survives the round trip.
	if !strings.Contains(last.UsageRaw, `"service_tier":"standard"`) {
		t.Errorf("verbatim usage payload lost: %q", last.UsageRaw)
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
	if last.EngineVersion != "Codex CLI 0.144.0" {
		t.Errorf("engine_version = %q", last.EngineVersion)
	}
	// Cost is the one float column, so it round-trips through its own SQL
	// literal formatter and scan path; sub-cent precision must survive both.
	if last.CostUSD != 0.6231 {
		t.Errorf("cost_usd = %v, want 0.6231", last.CostUSD)
	}
	// Both spend figures round-trip, and the reported one wins: an estimate
	// must never displace what the engine itself said the run cost.
	if last.EstCostUSD != 0.4177 {
		t.Errorf("est_cost_usd = %v, want 0.4177", last.EstCostUSD)
	}
	if last.EffectiveCostUSD() != 0.6231 || last.CostEstimated() {
		t.Errorf("effective cost = %v estimated=%v, want the reported 0.6231",
			last.EffectiveCostUSD(), last.CostEstimated())
	}
	all, err := s.ListReviews(ctx, 5)
	if err != nil || len(all) != 1 || all[0].Title != title {
		t.Errorf("ListReviews must carry the snapshot too: %+v err=%v", all, err)
	}
	// The SQL aggregate must sum the comparable figure, not the cache-inflated
	// total: Overview and the Metrics page read the same history and used to
	// disagree by everything cached.
	if total, err := s.FreshTokens(ctx, time.Time{}); err != nil || total != 42575 {
		t.Errorf("FreshTokens(all time) = %d err=%v, want 42575 (not the 192575 total)", total, err)
	}
	if recent, err := s.FreshTokens(ctx, time.Now().Add(-time.Hour)); err != nil || recent != 42575 {
		t.Errorf("FreshTokens(last hour) = %d err=%v, want 42575", recent, err)
	}
	if none, err := s.FreshTokens(ctx, time.Now().Add(time.Hour)); err != nil || none != 0 {
		t.Errorf("FreshTokens(future window) = %d err=%v, want 0", none, err)
	}
}

// TestHostileStringsRoundTrip drives GitHub-controlled strings through the
// real SQL path: quotes and injection shapes must store and read back intact.
func TestHostileStringsRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	title := `fix: O'Brien's "quote" bug'; DROP TABLE queue; --`
	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 9, Type: TypeNew, HeadSHA: "sha", Title: title, Author: "o'malley"}); err != nil {
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

	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 12, Type: TypeNew, HeadSHA: "sha", Source: SourceDiscovered}); err != nil {
		t.Fatal(err)
	}
	if c, _ := getQueued(t, s, "o/r", 12); c.Source != SourceDiscovered {
		t.Fatalf("fresh discovered row source = %q", c.Source)
	}
	// Human re-adds it: escalate.
	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 12, Type: TypeNew, HeadSHA: "sha", Source: SourceManual}); err != nil {
		t.Fatal(err)
	}
	if c, _ := getQueued(t, s, "o/r", 12); c.Source != SourceManual {
		t.Fatalf("manual re-add must escalate source, got %q", c.Source)
	}
	// Discovery sweeps again: must NOT downgrade.
	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 12, Type: TypeRefreshed, HeadSHA: "sha", Source: SourceDiscovered}); err != nil {
		t.Fatal(err)
	}
	if c, _ := getQueued(t, s, "o/r", 12); c.Source != SourceManual {
		t.Fatalf("discovery must not downgrade a manual row, got %q", c.Source)
	}
	// Empty source defaults to discovered.
	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 13, Type: TypeNew, HeadSHA: "sha"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := getQueued(t, s, "o/r", 13); c.Source != SourceDiscovered {
		t.Fatalf("empty source must default to discovered, got %q", c.Source)
	}
}

// TestListAuthorsAlphabetical: the list is about authors, so it comes
// back alphabetical by handle, case-insensitively (DuckDB's raw TEXT order
// would put "Zed" before "alice"), with repo as the tiebreak.
func TestListAuthorsAlphabetical(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, a := range []Author{
		{Repo: "org/b", GitHubHandle: "Zed", Group: "core"},
		{Repo: "org/a", GitHubHandle: "alice", Group: "core"},
		{Repo: WildcardRepo, GitHubHandle: "Bob", Group: "core"},
		{Repo: "org/a", GitHubHandle: "Bob", Group: "core"},
	} {
		if err := s.SetAuthorGroup(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	authors, err := s.ListAuthors(ctx, "", "")
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
// new discovery; discovered_at must keep its first-seen value, not track the
// latest sweep.
func TestEnqueueDiscoveredAtFirstSeen(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first := time.Now().UTC().Truncate(time.Second).Add(-3 * time.Hour)
	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 20, Type: TypeNew, HeadSHA: "sha", DiscoveredAt: first}); err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 20, Type: TypeNew, HeadSHA: "sha", DiscoveredAt: time.Now()}); err != nil {
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
		if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 21, Type: TypeNew, HeadSHA: "sha", EligibleAt: eligible, HoldReason: reason, Source: source}); err != nil {
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

	// A second claimant loses while the lease is live, and must not clobber
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

// TestRunningRuns: only status='running' rows surface; the reconciliation
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
// to manual: the one-write "review this now" action.
func TestPromote(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	eligible := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 22, Type: TypeNew, HeadSHA: "sha", EligibleAt: &eligible, HoldReason: HoldCooldown}); err != nil {
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

// TestInitMigratesPreExistingHistory runs Init against a store shaped the way
// v0.21.1 left it: a populated codex_version column and no engine_version.
//
// Every other test here starts from an empty temp store, where CREATE TABLE
// already has the current columns and the migration block is a no-op — so the
// path that actually matters (real history, a backfill, and a column DROP)
// was never exercised. The schema comment notes this add/backfill/drop shape
// is likely to recur for future renames, which is exactly why it needs a
// regression test rather than a one-off manual check.
func TestInitMigratesPreExistingHistory(t *testing.T) {
	if _, err := exec.LookPath("duckdb"); err != nil {
		t.Skip("duckdb CLI not on PATH")
	}
	path := filepath.Join(t.TempDir(), "legacy.duckdb")

	// Seed the old shape directly, bypassing Store so no current-schema DDL runs.
	seed := `CREATE TABLE history (
	  repo TEXT NOT NULL, number INTEGER NOT NULL, title TEXT, author TEXT,
	  head_sha TEXT NOT NULL, verdict TEXT NOT NULL, engine TEXT, model TEXT,
	  effort TEXT, codex_version TEXT, reviewed_at TIMESTAMP NOT NULL,
	  duration_secs INTEGER NOT NULL DEFAULT 0, work_dir TEXT,
	  tokens_used INTEGER NOT NULL DEFAULT 0);
	INSERT INTO history VALUES ('o/r', 7, 'legacy row', 'someone', 'sha7',
	  'APPROVED', 'codex', 'gpt-5.6', 'high', 'codex-cli 0.144.5', now(), 42,
	  '/tmp/wd', 1000);
	INSERT INTO history VALUES ('o/r', 8, 'legacy claude row', 'someone', 'sha8',
	  'APPROVED', 'claude', 'claude-opus-5', 'medium', 'claude 2.1.0', now(), 500,
	  '/tmp/wd8', 3700000);
	CREATE TABLE queue (repo TEXT NOT NULL, number INTEGER NOT NULL,
	  type TEXT NOT NULL, PRIMARY KEY (repo, number));`
	if out, err := exec.Command("duckdb", path, "-c", seed).CombinedOutput(); err != nil {
		t.Fatalf("seed legacy store: %v\n%s", err, out)
	}

	s, err := Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()
	// Init twice: the migration must be idempotent, since it runs every boot.
	for i := range 2 {
		if err := s.Init(ctx); err != nil {
			t.Fatalf("Init #%d: %v", i+1, err)
		}
	}

	reviews, err := s.ListReviews(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 2 {
		t.Fatalf("history rows = %d, want the seeded rows to survive", len(reviews))
	}
	byEngine := map[string]Review{}
	for _, r := range reviews {
		byEngine[r.Engine] = r
	}
	// The value must have been carried across, not dropped with the column.
	if got := byEngine["codex"].EngineVersion; got != "codex-cli 0.144.5" {
		t.Errorf("engine_version = %q, want the backfilled codex_version", got)
	}
	if byEngine["codex"].TokensUsed != 1000 || byEngine["codex"].Title != "legacy row" {
		t.Errorf("unrelated columns disturbed: %+v", byEngine["codex"])
	}
	// The fresh_tokens backfill turns on what an engine's total meant before
	// the column existed. codex never counted cached re-reads, so its total IS
	// the fresh count; claude's was cache-inflated and its split is gone, so
	// the row must read as unknown rather than charting at its inflated total.
	if got := byEngine["codex"].FreshTokens; got != 1000 {
		t.Errorf("codex fresh_tokens = %d, want its total 1000 backfilled", got)
	}
	if got := byEngine["claude"].FreshTokens; got != 0 {
		t.Errorf("claude fresh_tokens = %d, want 0 (unknown): its 3.7M total is cache-inflated", got)
	}
	if got := byEngine["claude"].TokensUsed; got != 3_700_000 {
		t.Errorf("claude tokens_used = %d, want the recorded total kept", got)
	}

	// And the retired column is gone rather than lingering.
	out, err := exec.Command("duckdb", path, "-c",
		"SELECT count(*) FROM information_schema.columns WHERE table_name='history' AND column_name='codex_version';").CombinedOutput()
	if err != nil {
		t.Fatalf("column check: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "0") {
		t.Errorf("codex_version still present after migration:\n%s", out)
	}
}

// The legacy recovery UPDATE folds input+output+cache_creation into
// fresh_tokens, but a row the current drivers wrote also carries
// cache_write_tokens, which that sum does not know about. Re-deriving such a
// row would silently drop its cache writes, so the guard restricting the
// UPDATE to fresh_tokens = 0 rows is load-bearing and runs on every boot.
func TestInitLeavesModernTokenRowsAlone(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	c := Candidate{Repo: "o/r", Number: 9, Type: TypeNew, HeadSHA: "sha9"}
	if err := s.Enqueue(ctx, c); err != nil {
		t.Fatal(err)
	}
	rec := ReviewFrom(c, VerdictApproved, "claude", time.Time{})
	rec.TokensUsed, rec.FreshTokens = 1000, 900
	rec.InputTokens, rec.OutputTokens, rec.CacheWriteTokens, rec.CacheReadTokens = 400, 100, 400, 100
	if err := s.Complete(ctx, rec); err != nil {
		t.Fatal(err)
	}

	// Init runs the whole migration block again, as every boot does.
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}

	after, ok, err := s.LastOutcome(ctx, "o/r", 9)
	if err != nil || !ok {
		t.Fatalf("row missing: ok=%v err=%v", ok, err)
	}
	if after.FreshTokens != 900 {
		t.Errorf("fresh_tokens = %d, want 900 untouched: the legacy recovery re-derived it and lost the 400 cache writes", after.FreshTokens)
	}
	if after.CacheWriteTokens != 400 {
		t.Errorf("cache_write_tokens = %d, want 400", after.CacheWriteTokens)
	}
}

// EstimateCosts fills gaps and only gaps: a row already valued keeps its
// figure, so a later run at newer rates cannot rewrite what a past review
// cost. A model the table does not list stays unpriced rather than free.
func TestEstimateCostsFillsOnlyTheGaps(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	add := func(number int, model string, est float64, in, out, cw, cr int) {
		c := Candidate{Repo: "o/r", Number: number, Type: TypeNew, HeadSHA: fmt.Sprintf("sha%d", number)}
		if err := s.Enqueue(ctx, c); err != nil {
			t.Fatal(err)
		}
		rec := ReviewFrom(c, VerdictApproved, "codex", time.Time{})
		rec.Model, rec.EstCostUSD = model, est
		rec.InputTokens, rec.OutputTokens, rec.CacheWriteTokens, rec.CacheReadTokens = in, out, cw, cr
		if err := s.Complete(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	add(1, "priced-model", 0, 100_000, 10_000, 0, 200_000) // needs a valuation
	add(2, "priced-model", 9.99, 100_000, 10_000, 0, 0)    // already valued
	add(3, "unlisted-model", 0, 50_000, 5_000, 0, 0)       // no rates for it
	add(4, "priced-model", 0, 0, 0, 0, 0)                  // no split to price

	models, err := s.UnpricedModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Only rows that could be valued and have not been: not the valued one,
	// not the one with no split.
	if got := len(models); got != 2 {
		t.Errorf("unpriced models = %v, want the two with a split and no estimate", models)
	}

	n, err := s.EstimateCosts(ctx, map[string]CostRates{
		"priced-model": {Input: 2e-06, Output: 1e-05, CacheRead: 2e-07},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("valued %d rows, want just the one gap", n)
	}

	byNumber := map[int]Review{}
	all, err := s.ListReviews(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range all {
		byNumber[r.Number] = r
	}
	// 100k*2e-6 + 10k*1e-5 + 200k*2e-7 = 0.2 + 0.1 + 0.04
	if got := byNumber[1].EstCostUSD; math.Abs(got-0.34) > 1e-9 {
		t.Errorf("gap row valued %v, want 0.34", got)
	}
	if got := byNumber[2].EstCostUSD; got != 9.99 {
		t.Errorf("already-valued row = %v, want its 9.99 kept", got)
	}
	if got := byNumber[3].EstCostUSD; got != 0 {
		t.Errorf("unlisted model = %v, want no estimate rather than a zero-priced one", got)
	}
}

// AppendHistory differs from Complete in exactly one way that matters: the
// queue row survives. An abandoned review still has work pending, so retiring
// its row would be the black hole the record exists to prevent.
func TestAppendHistoryLeavesTheQueueRowPending(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	c := Candidate{Repo: "o/r", Number: 42, Type: TypeNew, HeadSHA: "sha42"}
	if err := s.Enqueue(ctx, c); err != nil {
		t.Fatal(err)
	}
	rec := ReviewFrom(c, VerdictError, EngineAbandoned, time.Now().Add(-3*time.Minute))
	rec.WorkDir = "/tmp/interrupted-workdir"
	if err := s.AppendHistory(ctx, rec); err != nil {
		t.Fatal(err)
	}

	queued, err := s.ListQueue(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 {
		t.Fatalf("queue rows = %d, want the pending work to survive the record", len(queued))
	}

	last, ok, err := s.LastOutcome(ctx, "o/r", 42)
	if err != nil || !ok {
		t.Fatalf("history row missing: ok=%v err=%v", ok, err)
	}
	// The work dir is the point: it is what keeps the interrupted run's
	// transcript reachable once the next claim allocates a fresh one.
	if last.WorkDir != "/tmp/interrupted-workdir" {
		t.Errorf("work_dir = %q, want the interrupted run's kept", last.WorkDir)
	}
	if last.DurationSecs <= 0 {
		t.Errorf("duration = %d, want the time spent before the interruption", last.DurationSecs)
	}
}

// TestInitMigratesPreGroupsRoster runs Init against an allowed_authors table
// shaped the way it was before groups existed: no group_name column at all.
//
// Every other roster test starts from a temp store whose CREATE TABLE already
// has the column, so the migration block is a no-op there and the path that
// actually matters was never exercised. This is the one that runs against a
// live database on upgrade, so the guarantee it carries (existing rows keep
// meaning exactly what they meant, with no user action) needs a regression
// test rather than a manual check.
func TestInitMigratesPreGroupsRoster(t *testing.T) {
	if _, err := exec.LookPath("duckdb"); err != nil {
		t.Skip("duckdb CLI not on PATH")
	}
	path := filepath.Join(t.TempDir(), "pre-groups.duckdb")

	// Seed the old shape directly, bypassing Store so no current-schema DDL runs.
	seed := `CREATE TABLE allowed_authors (
	  repo TEXT NOT NULL, github_handle TEXT NOT NULL, name TEXT, email TEXT,
	  slack_id TEXT, PRIMARY KEY (repo, github_handle));
	INSERT INTO allowed_authors VALUES ('org/repo-a', 'alice', 'Alice A', 'a@example.com', 'U1');
	INSERT INTO allowed_authors VALUES ('*', 'bob', NULL, NULL, NULL);`
	if out, err := exec.Command("duckdb", path, "-c", seed).CombinedOutput(); err != nil {
		t.Fatalf("seed pre-groups store: %v\n%s", err, out)
	}

	s, err := Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()
	// Init twice: the migration must be idempotent, since it runs every boot.
	for i := range 2 {
		if err := s.Init(ctx); err != nil {
			t.Fatalf("Init #%d: %v", i+1, err)
		}
	}

	authors, err := s.ListAuthors(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 2 {
		t.Fatalf("roster rows = %d, want both seeded rows to survive", len(authors))
	}
	for _, a := range authors {
		// The allow list meant exactly one thing, so the backfill renames an
		// implicit policy rather than making a new decision.
		if a.Group != config.GroupApprover {
			t.Errorf("%s@%s backfilled to %q, want %q", a.GitHubHandle, a.Repo, a.Group, config.GroupApprover)
		}
	}
	if authors[0].GitHubHandle != "alice" || authors[0].Name != "Alice A" ||
		authors[0].Email != "a@example.com" || authors[0].SlackID != "U1" {
		t.Errorf("unrelated columns disturbed: %+v", authors[0])
	}

	// And the lookup that gates approval agrees, on both the repo-keyed row
	// and the wildcard one.
	for _, tc := range []struct{ repo, handle string }{{"org/repo-a", "alice"}, {"org/other", "bob"}} {
		m, err := s.AuthorGroup(ctx, tc.repo, tc.handle)
		if err != nil {
			t.Fatal(err)
		}
		if m.Group != config.GroupApprover {
			t.Errorf("AuthorGroup(%q, %q) = %+v, want the approver group", tc.repo, tc.handle, m)
		}
		// An upgraded store must still approve exactly who it approved before.
		if !(config.Config{}).ResolvePolicy(tc.repo, tc.handle, m).MayApprove() {
			t.Errorf("%s@%s lost approvability across the upgrade", tc.handle, tc.repo)
		}
	}
}

// TestAuthorGroupMatchesRepoCaseInsensitively: GitHub treats repo names as
// case-insensitive and every other comparison in this codebase follows suit
// (config.RepoMatches, lookupRepo, WatchesRepo, AuthorScopedRepo). The roster
// queries lowered the handle but compared the repo exactly, so a row written
// as "Org/Repo" was invisible to a lookup for "org/repo" and the author fell
// through to authors.unlisted instead: a silent policy change, in the
// direction of quietly losing an approve-level group.
func TestAuthorGroupMatchesRepoCaseInsensitively(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.SetAuthorGroup(ctx, Author{Repo: "Org/Repo-A", GitHubHandle: "alice", Group: "core"}); err != nil {
		t.Fatal(err)
	}

	// The repo as discovery would spell it, from config.
	got, err := s.AuthorGroup(ctx, "org/repo-a", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if got.Group != "core" {
		t.Errorf("AuthorGroup with differently-cased repo = %+v, want the core row", got)
	}

	// The listing filter and the delete must agree with the lookup, or the
	// roster can be seen but not managed.
	authors, err := s.ListAuthors(ctx, "org/repo-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 1 {
		t.Errorf("ListAuthors with differently-cased repo = %+v, want the one row", authors)
	}
	if err := s.RemoveAuthor(ctx, "ORG/REPO-A", "alice"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.AuthorGroup(ctx, "Org/Repo-A", "alice"); got.Group != "" {
		t.Errorf("RemoveAuthor with differently-cased repo left the row: %+v", got)
	}
}

// The wildcard row must not be reachable by a repo literally named "*" in a
// different case, and an exact repo row must still beat the wildcard when
// only their case differs from the query.
func TestAuthorGroupRepoPrecedenceSurvivesCaseFolding(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, a := range []Author{
		{Repo: WildcardRepo, GitHubHandle: "bob", Group: "outsider"},
		{Repo: "Org/Repo-A", GitHubHandle: "bob", Group: "core"},
	} {
		if err := s.SetAuthorGroup(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.AuthorGroup(ctx, "org/repo-a", "BOB")
	if err != nil {
		t.Fatal(err)
	}
	if got.Group != "core" {
		t.Errorf("exact repo row must win over the wildcard even when case differs, got %+v", got)
	}
}
