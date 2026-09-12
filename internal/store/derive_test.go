package store

import (
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/score"
)

func reviewAt(head, diffSHA, verdict string, adds, dels int) Review {
	return Review{
		Repo: "o/r", Number: 1, Author: "alice", HeadSHA: head, Verdict: verdict,
		Diff: DiffStats{ScoredAdditions: adds, ScoredDeletions: dels, DiffSHA: diffSHA},
	}
}

func TestDeriveScoreFreezesTheRecord(t *testing.T) {
	rules := score.DefaultRules()
	at := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)

	got, ok := DeriveScore(rules, ScoreContext{Attempt: 1}, reviewAt("sha", "sha", VerdictApproved, 40, 10), at)
	if !ok {
		t.Fatal("a clean review should be scorable")
	}
	if got.Points() != 132 {
		t.Errorf("score = %d, want 132", got.Points())
	}
	if got.Source != ScoreDerived || got.Rules != rules.Hash() {
		t.Errorf("provenance = %+v, want derived under the current ruleset", got)
	}
	if got.Attempt == nil || *got.Attempt != 1 || !got.At.Equal(at) {
		t.Errorf("attempt/at = %v/%v", got.Attempt, got.At)
	}
}

// THE regression this function exists for.
//
// The scheduler declined to score a review whose head moved mid-run, but left
// the diff figures on the row, and those rows are exactly what
// `recompute --missing` selects. Recompute had no such guard, so it scored
// them off a diff describing code the review never saw. Both paths now derive
// through here, so neither can forget it.
func TestDeriveScoreRefusesAStaleDiff(t *testing.T) {
	r := reviewAt("sha-reviewed", "sha-newer", VerdictApproved, 40, 10)
	if _, ok := DeriveScore(score.DefaultRules(), ScoreContext{Attempt: 1}, r, time.Now()); ok {
		t.Error("a diff describing another revision must not be scored")
	}

	// Same row once the figures match the reviewed head.
	r.Diff.DiffSHA = "sha-reviewed"
	if _, ok := DeriveScore(score.DefaultRules(), ScoreContext{Attempt: 1}, r, time.Now()); !ok {
		t.Error("matching figures should score normally")
	}
}

// A diff that was never fetched must leave the row unscored, NOT scored zero.
//
// The zeroed counts on such a row are churn 0, which is a legitimate score of
// nothing for a PR whose every line is generated. Reading them the same way
// froze a 0 onto a real PR whose fetch had merely been rate-limited, and
// because the row then looked scored, `--missing` never came back for it.
func TestDeriveScoreRefusesAnUnfetchedDiff(t *testing.T) {
	r := reviewAt("sha", "", VerdictApproved, 40, 10)
	if _, ok := DeriveScore(score.DefaultRules(), ScoreContext{Attempt: 1}, r, time.Now()); ok {
		t.Error("a review with no recorded diff must stay unscored and recoverable")
	}

	// A genuinely empty diff IS scored, at zero: the figures were fetched and
	// they really are nothing, which is the fully-generated-PR case.
	empty := reviewAt("sha", "sha", VerdictApproved, 0, 0)
	got, ok := DeriveScore(score.DefaultRules(), ScoreContext{Attempt: 1}, empty, time.Now())
	if !ok {
		t.Fatal("a fetched, genuinely empty diff should be scored")
	}
	if got.Points() != 0 {
		t.Errorf("score = %d, want 0", got.Points())
	}
}

func TestDeriveScoreZeroesADiscussionRereview(t *testing.T) {
	r := reviewAt("sha", "sha", VerdictApproved, 40, 10)
	got, ok := DeriveScore(score.DefaultRules(), ScoreContext{Attempt: 1, ReviewedAtThisHead: true}, r, time.Now())
	if !ok {
		t.Fatal("a re-review is still scored, with the answer zero")
	}
	if got.Points() != 0 {
		t.Errorf("score = %d, want 0: replying to the bot is not new work", got.Points())
	}
}

func TestDeriveScoreRefusesNonRealVerdicts(t *testing.T) {
	for _, v := range []string{VerdictSkipped, VerdictError, VerdictWorking, ""} {
		r := reviewAt("sha", "sha", v, 40, 10)
		if _, ok := DeriveScore(score.DefaultRules(), ScoreContext{Attempt: 1}, r, time.Now()); ok {
			t.Errorf("verdict %q must not be scored", v)
		}
	}
}

// The attempt index drives the decay, so it has to reach Compute intact.
func TestDeriveScoreAppliesTheAttemptDecay(t *testing.T) {
	r := reviewAt("sha", "sha", VerdictApproved, 40, 10)
	first, _ := DeriveScore(score.DefaultRules(), ScoreContext{Attempt: 1}, r, time.Now())
	third, _ := DeriveScore(score.DefaultRules(), ScoreContext{Attempt: 3}, r, time.Now())
	if third.Points() >= first.Points() {
		t.Errorf("attempt 3 scored %d against attempt 1's %d: the decay must apply", third.Points(), first.Points())
	}
	if *third.Attempt != 3 {
		t.Errorf("attempt = %d, want the index it was derived at", *third.Attempt)
	}
}

// Retuning must change the recorded hash, or a rescored row would claim to be
// current under rules it was never scored with.
func TestDeriveScoreRecordsTheRulesetItUsed(t *testing.T) {
	r := reviewAt("sha", "sha", VerdictApproved, 40, 10)
	tuned := score.DefaultRules()
	tuned.Base = 250

	base, _ := DeriveScore(score.DefaultRules(), ScoreContext{Attempt: 1}, r, time.Now())
	other, _ := DeriveScore(tuned, ScoreContext{Attempt: 1}, r, time.Now())
	if base.Rules == other.Rules {
		t.Error("a different ruleset must record a different hash")
	}
	if base.Points() == other.Points() {
		t.Error("a different base must produce a different score")
	}
}
