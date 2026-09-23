//go:build integration

package cli

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/shhac/crew-code-review/internal/store"
)

func newIntegrationStore(t *testing.T) store.Store {
	t.Helper()
	if _, err := exec.LookPath("duckdb"); err != nil {
		t.Skip("duckdb CLI not on PATH")
	}
	s, err := store.Open("duckdb", filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

// A manual correction is only a correction if the leaderboard reads it. The
// newest history row is the wrong target twice over: a discussion re-review at
// the same head is dropped by the leaderboard's one-row-per-revision rule, and
// a skipped run is not a review at all but was counted as one once it carried
// points.
func TestScoreSetIsWhatTheLeaderboardCounts(t *testing.T) {
	zero := 0
	for _, tc := range []struct {
		name string
		last store.Review
	}{
		{"after a discussion re-review", store.Review{
			Verdict: store.VerdictCommented, HeadSHA: "h1",
			Score: store.ScoreRecord{Score: &zero, Source: store.ScoreDerived},
		}},
		{"after a skipped run", store.Review{Verdict: store.VerdictSkipped, HeadSHA: "h2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newIntegrationStore(t)
			ctx := context.Background()
			base := time.Now().Add(-time.Hour)
			fifty := 50
			first := store.Review{
				Repo: "o/r", Number: 1, Author: "alice", HeadSHA: "h1", Verdict: store.VerdictApproved,
				Engine: "codex", ReviewedAt: base, Score: store.ScoreRecord{Score: &fifty, Source: store.ScoreDerived},
			}
			last := tc.last
			last.Repo, last.Number, last.Author, last.Engine = "o/r", 1, "alice", "codex"
			last.ReviewedAt = base.Add(time.Minute)
			for _, r := range []store.Review{first, last} {
				if err := s.AppendHistory(ctx, r); err != nil {
					t.Fatal(err)
				}
			}

			if err := setScore(ctx, s, "o/r", 1, 80, "why"); err != nil {
				t.Fatal(err)
			}
			board, err := s.Leaderboard(ctx, store.LeaderboardQuery{})
			if err != nil {
				t.Fatal(err)
			}
			if len(board) != 1 || board[0].Total != 80 || board[0].Reviews != 1 {
				t.Errorf("leaderboard = %+v, want alice on 80 from one review", board)
			}
		})
	}
}
