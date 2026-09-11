//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/score"
)

func ptr(i int) *int { return &i }

// completeScored enqueues, claims and completes a PR with a frozen score.
func completeScored(t *testing.T, s Store, repo string, number int, author, verdict string, at time.Time, rec ScoreRecord, diff DiffStats) Review {
	t.Helper()
	ctx := context.Background()
	head := diff.DiffSHA
	if head == "" {
		head = "sha-default"
	}
	if err := s.Enqueue(ctx, Candidate{
		Repo: repo, Number: number, Type: TypeNew, Author: author, HeadSHA: head,
		DiscoveredAt: at, Additions: diff.Additions, Deletions: diff.Deletions, ChangedFiles: diff.ChangedFiles,
	}); err != nil {
		t.Fatal(err)
	}
	r := Review{
		Repo: repo, Number: number, Author: author, HeadSHA: head, Verdict: verdict,
		Engine: "codex", ReviewedAt: at, Diff: diff, Score: rec,
	}
	if err := s.Complete(ctx, r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestScoreRoundTripsThroughHistory(t *testing.T) {
	s := newTestStore(t)
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	diff := DiffStats{Additions: 12000, Deletions: 11000, ChangedFiles: 4, ScoredAdditions: 40, ScoredDeletions: 10, ExcludedFiles: 2, DiffSHA: "sha-a"}
	completeScored(t, s, "o/r", 1, "alice", VerdictApproved, at,
		ScoreRecord{Score: ptr(150), Source: ScoreDerived, Rules: "abc123", Attempt: ptr(1), At: at}, diff)

	got, ok, err := s.LastOutcome(context.Background(), "o/r", 1)
	if err != nil || !ok {
		t.Fatalf("LastOutcome: %v ok=%v", err, ok)
	}
	if !got.Score.Scored() || got.Score.Points() != 150 {
		t.Errorf("score = %+v, want 150", got.Score)
	}
	if got.Score.Source != ScoreDerived || got.Score.Rules != "abc123" {
		t.Errorf("provenance = %+v", got.Score)
	}
	if got.Score.Attempt == nil || *got.Score.Attempt != 1 {
		t.Errorf("attempt = %v, want 1", got.Score.Attempt)
	}
	// The raw figures must survive alongside the scored ones, or the row
	// cannot explain why it disagrees with the PR page.
	if got.Diff != diff {
		t.Errorf("diff = %+v, want %+v", got.Diff, diff)
	}
}

// NULL (never scored) and 0 (scored, worth nothing) are different facts and
// must stay distinguishable through a write and a read.
func TestUnscoredIsDistinctFromZero(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Hour).Truncate(time.Second)

	completeScored(t, s, "o/r", 1, "alice", VerdictApproved, at, ScoreRecord{}, DiffStats{DiffSHA: "sha-1"})
	completeScored(t, s, "o/r", 2, "alice", VerdictApproved, at,
		ScoreRecord{Score: ptr(0), Source: ScoreDerived, Rules: "h", Attempt: ptr(1)}, DiffStats{DiffSHA: "sha-2"})

	unscored, _, _ := s.LastOutcome(ctx, "o/r", 1)
	if unscored.Score.Scored() {
		t.Error("a row written with no score must read back unscored, not as zero")
	}
	zero, _, _ := s.LastOutcome(ctx, "o/r", 2)
	if !zero.Score.Scored() || zero.Score.Points() != 0 {
		t.Errorf("a deliberate zero must read back as a score of zero, got %+v", zero.Score)
	}

	// And a sweep looking for unscored rows must find exactly the first.
	todo, err := s.ReviewsToScore(ctx, ScoreQuery{Missing: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(todo) != 1 || todo[0].Number != 1 {
		t.Errorf("ReviewsToScore(Missing) returned %d rows, want only PR 1", len(todo))
	}
}

// Attempts count REVISIONS, not verdicts: a discussion re-review is a second
// verdict at the same head and must not cost the author a decay step.
func TestScoreContextCountsRevisionsNotVerdicts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Now().Add(-10 * time.Hour).Truncate(time.Second)

	// Two reviews at head A, then the author pushes and we review head B.
	completeScored(t, s, "o/r", 1, "alice", VerdictCommented, base, ScoreRecord{Score: ptr(10)}, DiffStats{DiffSHA: "sha-A"})
	completeScored(t, s, "o/r", 1, "alice", VerdictCommented, base.Add(time.Hour), ScoreRecord{Score: ptr(0)}, DiffStats{DiffSHA: "sha-A"})

	atB := base.Add(2 * time.Hour)
	gotB, err := s.ScoreContext(ctx, "o/r", 1, "sha-B", atB)
	if err != nil {
		t.Fatal(err)
	}
	if gotB.Attempt != 2 {
		t.Errorf("attempt at the second revision = %d, want 2 (two verdicts, but only one earlier head)", gotB.Attempt)
	}
	if gotB.ReviewedAtThisHead {
		t.Error("head B has not been reviewed before")
	}

	// A third verdict back at head A is a discussion re-review: same index,
	// and flagged so it pays out nothing.
	gotA, err := s.ScoreContext(ctx, "o/r", 1, "sha-A", base.Add(90*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if gotA.Attempt != 1 {
		t.Errorf("attempt back at head A = %d, want 1", gotA.Attempt)
	}
	if !gotA.ReviewedAtThisHead {
		t.Error("head A HAS been reviewed before; the flag must say so")
	}
}

// SKIPPED and ERROR are our machinery's outcomes, not the author's work.
func TestScoreContextIgnoresNonRealVerdicts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Now().Add(-5 * time.Hour).Truncate(time.Second)

	for i, v := range []string{VerdictSkipped, VerdictError} {
		completeScored(t, s, "o/r", 1, "alice", v, base.Add(time.Duration(i)*time.Minute), ScoreRecord{}, DiffStats{DiffSHA: "sha-" + v})
	}
	got, err := s.ScoreContext(ctx, "o/r", 1, "sha-new", base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got.Attempt != 1 {
		t.Errorf("attempt = %d, want 1: skipped and errored rows are not attempts", got.Attempt)
	}
}

func TestScoreContextOnAFreshPR(t *testing.T) {
	got, err := newTestStore(t).ScoreContext(context.Background(), "o/r", 99, "sha", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Attempt != 1 || got.ReviewedAtThisHead {
		t.Errorf("ScoreContext on an unreviewed PR = %+v, want attempt 1", got)
	}
}

// A manual correction must survive a retune, or it is not a correction.
func TestManualScoreSurvivesRecomputeSelection(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Hour).Truncate(time.Second)

	r := completeScored(t, s, "o/r", 1, "alice", VerdictApproved, at,
		ScoreRecord{Score: ptr(150), Source: ScoreDerived, Rules: "oldhash", Attempt: ptr(1)}, DiffStats{DiffSHA: "sha-1"})

	if err := s.SetReviewScore(ctx, r.Ref(), ScoreRecord{
		Score: ptr(0), Source: ScoreManual, Rules: "oldhash", Note: "duplicate of #2", Attempt: ptr(1),
	}); err != nil {
		t.Fatal(err)
	}

	got, _, _ := s.LastOutcome(ctx, "o/r", 1)
	if got.Score.Points() != 0 || got.Score.Source != ScoreManual || got.Score.Note != "duplicate of #2" {
		t.Fatalf("manual override did not land: %+v", got.Score)
	}

	// A stale-rules sweep must leave it alone...
	stale, err := s.ReviewsToScore(ctx, ScoreQuery{StaleRules: map[string]string{"": "newhash"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 0 {
		t.Errorf("a manual score was selected for recompute: %+v", stale[0].Score)
	}
	// ...unless it is explicitly asked for.
	forced, err := s.ReviewsToScore(ctx, ScoreQuery{StaleRules: map[string]string{"": "newhash"}, IncludeManual: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(forced) != 1 {
		t.Errorf("IncludeManual selected %d rows, want 1", len(forced))
	}
}

// The row is addressed by a natural key, not an enforced one, so the write
// must notice when it does not identify exactly one row.
func TestSetReviewScoreRefusesWhenItMatchesNoRow(t *testing.T) {
	s := newTestStore(t)
	err := s.SetReviewScore(context.Background(),
		ReviewRef{Repo: "o/r", Number: 1, ReviewedAt: time.Now()}, ScoreRecord{Score: ptr(1)})
	if err == nil {
		t.Fatal("scoring a row that does not exist should error")
	}
}

func TestLeaderboardAggregatesPerAuthor(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Now().Add(-48 * time.Hour).Truncate(time.Second)

	rows := []struct {
		number  int
		author  string
		verdict string
		score   int
		adds    int
		dels    int
	}{
		{1, "alice", VerdictApproved, 150, 40, 10},
		{2, "alice", VerdictCommented, 38, 40, 10},
		{3, "bob", VerdictApproved, 100, 200, 100},
		{4, "alice", VerdictApproved, 60, 0, 800},
	}
	for i, r := range rows {
		completeScored(t, s, "o/r", r.number, r.author, r.verdict, base.Add(time.Duration(i)*time.Minute),
			ScoreRecord{Score: ptr(r.score), Source: ScoreDerived, Rules: "h", Attempt: ptr(1)},
			DiffStats{ScoredAdditions: r.adds, ScoredDeletions: r.dels, DiffSHA: "sha-" + r.author})
	}
	// An unscored row must not dilute anybody's totals.
	completeScored(t, s, "o/r", 5, "alice", VerdictApproved, base.Add(time.Hour), ScoreRecord{}, DiffStats{DiffSHA: "sha-none"})

	board, err := s.Leaderboard(ctx, LeaderboardQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(board) != 2 {
		t.Fatalf("got %d authors, want 2: %+v", len(board), board)
	}
	if board[0].Author != "alice" || board[0].Total != 248 {
		t.Errorf("top = %+v, want alice with 248", board[0])
	}
	if board[0].Reviews != 3 {
		t.Errorf("alice reviews = %d, want 3 scored rows (the unscored one must not count)", board[0].Reviews)
	}
	if board[0].Approvals != 2 {
		t.Errorf("alice approvals = %d, want 2", board[0].Approvals)
	}
	if board[0].Additions != 80 || board[0].Deletions != 820 {
		t.Errorf("alice churn = +%d/-%d, want +80/-820", board[0].Additions, board[0].Deletions)
	}
	if board[1].Author != "bob" || board[1].Total != 100 {
		t.Errorf("second = %+v, want bob with 100", board[1])
	}
}

func TestLeaderboardNarrowing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	old := time.Now().Add(-30 * 24 * time.Hour).Truncate(time.Second)
	recent := time.Now().Add(-time.Hour).Truncate(time.Second)

	completeScored(t, s, "o/one", 1, "alice", VerdictApproved, old,
		ScoreRecord{Score: ptr(500), Source: ScoreDerived, Attempt: ptr(1)}, DiffStats{DiffSHA: "a"})
	completeScored(t, s, "o/two", 2, "alice", VerdictApproved, recent,
		ScoreRecord{Score: ptr(70), Source: ScoreDerived, Attempt: ptr(1)}, DiffStats{DiffSHA: "b"})

	byRepo, err := s.Leaderboard(ctx, LeaderboardQuery{Repo: "o/two"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byRepo) != 1 || byRepo[0].Total != 70 {
		t.Errorf("repo-narrowed board = %+v, want only o/two's 70", byRepo)
	}

	bySince, err := s.Leaderboard(ctx, LeaderboardQuery{Since: time.Now().Add(-24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(bySince) != 1 || bySince[0].Total != 70 {
		t.Errorf("time-narrowed board = %+v, want only the recent 70", bySince)
	}
}

// Negative totals are reachable by design (requested_changes is negative), so
// the aggregate must carry them rather than clamping.
func TestLeaderboardCarriesNegativeTotals(t *testing.T) {
	s := newTestStore(t)
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	completeScored(t, s, "o/r", 1, "carol", VerdictRequestedChanges, at,
		ScoreRecord{Score: ptr(-38), Source: ScoreDerived, Attempt: ptr(1)}, DiffStats{DiffSHA: "sha"})

	board, err := s.Leaderboard(context.Background(), LeaderboardQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(board) != 1 || board[0].Total != -38 {
		t.Errorf("board = %+v, want carol at -38", board)
	}
}

// The queue carries discovery's cheap counts so the dashboard can show a size
// before a review has happened.
func TestQueueCarriesDiffCounts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.Enqueue(ctx, Candidate{
		Repo: "o/r", Number: 7, Type: TypeNew, HeadSHA: "sha", DiscoveredAt: time.Now(),
		Additions: 120, Deletions: 45, ChangedFiles: 6,
	}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.QueuedPR(ctx, "o/r", 7)
	if err != nil || !ok {
		t.Fatalf("QueuedPR: %v ok=%v", err, ok)
	}
	if got.Additions != 120 || got.Deletions != 45 || got.ChangedFiles != 6 {
		t.Errorf("counts = +%d/-%d over %d files, want +120/-45 over 6", got.Additions, got.Deletions, got.ChangedFiles)
	}
}

// Rules are per-repo by construction, so staleness has to be judged per repo.
// One global hash marked every row in an overridden repo permanently stale:
// scored under the override's hash, measured against the global one, selected
// and rewritten to the identical value on every sweep, forever.
func TestStaleRulesComparesPerRepo(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Hour).Truncate(time.Second)

	// o/plain is scored under the global ruleset; o/special under its own.
	completeScored(t, s, "o/plain", 1, "alice", VerdictApproved, at,
		ScoreRecord{Score: ptr(10), Source: ScoreDerived, Rules: "globalhash", Attempt: ptr(1)}, DiffStats{DiffSHA: "a"})
	completeScored(t, s, "o/special", 2, "bob", VerdictApproved, at,
		ScoreRecord{Score: ptr(20), Source: ScoreDerived, Rules: "specialhash", Attempt: ptr(1)}, DiffStats{DiffSHA: "b"})

	current := map[string]string{"": "globalhash", "o/special": "specialhash"}
	stale, err := s.ReviewsToScore(ctx, ScoreQuery{StaleRules: current})
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 0 {
		t.Errorf("both rows are current under their own repo's rules; got %d stale: %+v", len(stale), stale[0].Score)
	}

	// Retuning only the override must select only that repo's rows.
	retuned := map[string]string{"": "globalhash", "o/special": "specialhash-v2"}
	stale, err = s.ReviewsToScore(ctx, ScoreQuery{StaleRules: retuned})
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0].Repo != "o/special" {
		t.Errorf("retuning o/special should select exactly its row, got %+v", stale)
	}

	// And retuning the global rules must not disturb the overridden repo.
	retuned = map[string]string{"": "globalhash-v2", "o/special": "specialhash"}
	stale, err = s.ReviewsToScore(ctx, ScoreQuery{StaleRules: retuned})
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0].Repo != "o/plain" {
		t.Errorf("retuning the global rules should select only the unoverridden repo, got %+v", stale)
	}
}

// Points are per REVISION, so two scored rows at one head must pay once.
//
// Reachable without any scoring bug: a review outrunning its claim lease can
// be re-claimed, and if both workers resolve their ScoreContext before either
// writes history, neither sees the other and both derive a full score.
func TestLeaderboardPaysOncePerRevision(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Now().Add(-3 * time.Hour).Truncate(time.Second)

	// Two completions of the same PR at the SAME head, both fully scored.
	completeScored(t, s, "o/r", 1, "alice", VerdictApproved, at,
		ScoreRecord{Score: ptr(135), Source: ScoreDerived, Rules: "h", Attempt: ptr(1)}, DiffStats{ScoredAdditions: 40, DiffSHA: "sha-A"})
	completeScored(t, s, "o/r", 1, "alice", VerdictApproved, at.Add(time.Minute),
		ScoreRecord{Score: ptr(135), Source: ScoreDerived, Rules: "h", Attempt: ptr(1)}, DiffStats{ScoredAdditions: 40, DiffSHA: "sha-A"})

	board, err := s.Leaderboard(ctx, LeaderboardQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(board) != 1 {
		t.Fatalf("got %d authors, want 1", len(board))
	}
	if board[0].Total != 135 {
		t.Errorf("total = %d, want 135: one revision pays once", board[0].Total)
	}
	if board[0].Reviews != 1 {
		t.Errorf("reviews = %d, want 1", board[0].Reviews)
	}
	if board[0].Additions != 40 {
		t.Errorf("additions = %d, want 40 counted once", board[0].Additions)
	}
}

// A genuinely new revision is still paid for: the dedup is per head, not per PR.
func TestLeaderboardPaysForEachRevision(t *testing.T) {
	s := newTestStore(t)
	base := time.Now().Add(-3 * time.Hour).Truncate(time.Second)

	completeScored(t, s, "o/r", 1, "alice", VerdictCommented, base,
		ScoreRecord{Score: ptr(34), Source: ScoreDerived, Attempt: ptr(1)}, DiffStats{DiffSHA: "sha-A"})
	completeScored(t, s, "o/r", 1, "alice", VerdictApproved, base.Add(time.Hour),
		ScoreRecord{Score: ptr(81), Source: ScoreDerived, Attempt: ptr(2)}, DiffStats{DiffSHA: "sha-B"})

	board, err := s.Leaderboard(context.Background(), LeaderboardQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(board) != 1 || board[0].Total != 115 {
		t.Errorf("board = %+v, want 115 across two revisions", board)
	}
}

// The measurement is stored with the row so an exclusion policy can be
// re-applied later without asking GitHub again.
func TestMeasuredFilesRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Hour).Truncate(time.Second)

	files := []score.FileStat{
		{Path: "main.go", Additions: 40, Deletions: 10},
		{Path: "package-lock.json", Additions: 8000, Deletions: 2000, Generated: true},
	}
	r := Review{
		Repo: "o/r", Number: 1, Author: "alice", HeadSHA: "sha", Verdict: VerdictApproved,
		Engine: "codex", ReviewedAt: at, DiffFiles: files,
		Diff: DiffStats{ScoredAdditions: 40, ScoredDeletions: 10, ExcludedFiles: 1, DiffSHA: "sha"},
	}
	if err := s.Enqueue(ctx, Candidate{Repo: "o/r", Number: 1, Type: TypeNew, HeadSHA: "sha", DiscoveredAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, r); err != nil {
		t.Fatal(err)
	}

	got, err := s.ReviewFiles(ctx, r.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d files, want 2", len(got))
	}
	if got[1].Path != "package-lock.json" || !got[1].Generated {
		t.Errorf("the repo's verdict must survive the round trip, got %+v", got[1])
	}

	// And the policy can be re-applied offline, with no network.
	rules := score.DefaultRules()
	rules.UseGitattributes = false
	if totals := score.Recount(got, rules); totals.Additions != 8040 {
		t.Errorf("recount ignoring gitattributes = %+v, want everything counted", totals)
	}
}

// A row with no stored measurement reads back as nil rather than erroring, so
// recompute can tell "nothing to re-apply policy to" from a failure.
func TestReviewFilesAbsentReadsAsNil(t *testing.T) {
	s := newTestStore(t)
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	completeScored(t, s, "o/r", 2, "bob", VerdictApproved, at, ScoreRecord{}, DiffStats{DiffSHA: "sha"})

	got, err := s.ReviewFiles(context.Background(), ReviewRef{Repo: "o/r", Number: 2, ReviewedAt: at})
	if err != nil {
		t.Fatalf("an absent measurement must not error: %v", err)
	}
	if got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}
