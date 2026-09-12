package dashboard

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/store"
)

// The whole point of Score being a *int is that "never scored" and "scored,
// worth nothing" are different facts: a PR whose every line was generated
// legitimately earns zero. A page that rendered the two alike would report
// "0 pts" for a review nobody has been able to measure.
func TestHistoryReviewScoreProjection(t *testing.T) {
	at := time.Now()
	cases := []struct {
		name       string
		score      store.ScoreRecord
		wantNil    bool
		wantPoints int
		wantJSON   string
		wantNoJSON string
	}{
		{
			name: "never scored is absent, not zero", score: store.ScoreRecord{},
			wantNil: true, wantNoJSON: `"score"`,
		},
		{
			name:       "a deliberate zero survives as zero",
			score:      store.ScoreRecord{Score: ptr(0), Bucket: "tiny"},
			wantPoints: 0, wantJSON: `"score":0`,
		},
		{
			name:       "a negative score is carried, not clamped",
			score:      store.ScoreRecord{Score: ptr(-34), Bucket: "small"},
			wantPoints: -34, wantJSON: `"score":-34`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := historyReviewsOf([]store.Review{{
				Repo: "o/r", Number: 1, Author: "alice", Verdict: store.VerdictApproved,
				ReviewedAt: at, Score: tc.score,
			}})
			if len(got) != 1 {
				t.Fatalf("got %d rows, want 1", len(got))
			}
			row := got[0]
			if tc.wantNil {
				if row.Score != nil {
					t.Errorf("Score = %v, want nil for an unscored review", *row.Score)
				}
			} else {
				if row.Score == nil {
					t.Fatal("Score = nil, want a value")
				}
				if *row.Score != tc.wantPoints {
					t.Errorf("Score = %d, want %d", *row.Score, tc.wantPoints)
				}
			}

			b, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			raw := string(b)
			if tc.wantJSON != "" && !strings.Contains(raw, tc.wantJSON) {
				t.Errorf("JSON missing %s: %s", tc.wantJSON, raw)
			}
			if tc.wantNoJSON != "" && strings.Contains(raw, tc.wantNoJSON) {
				t.Errorf("JSON should omit %s for an unscored row: %s", tc.wantNoJSON, raw)
			}
		})
	}
}

// The bucket travels with the score because it is the one fact a reader cannot
// infer from the number.
func TestHistoryReviewCarriesTheBucket(t *testing.T) {
	got := historyReviewsOf([]store.Review{{
		Repo: "o/r", Number: 1, Verdict: store.VerdictApproved, ReviewedAt: time.Now(),
		Score: store.ScoreRecord{Score: ptr(135), Bucket: "small"},
	}})
	if got[0].ScoreBucket != "small" {
		t.Errorf("ScoreBucket = %q, want small", got[0].ScoreBucket)
	}
}
