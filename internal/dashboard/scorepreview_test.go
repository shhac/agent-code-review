package dashboard

import (
	"net/http"
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/score"
	"github.com/shhac/agent-code-review/internal/store"
)

func previewServer() *Server { return testServer(withConfig(config.Config{})) }

func preview(t *testing.T, query string) (int, scorePreviewResp) {
	t.Helper()
	return serveJSON[scorePreviewResp](t, previewServer().handleScorePreview,
		http.MethodGet, "/api/score/preview?"+query, "")
}

// The preview must be the scorer's own answer, not a lookalike. This is the
// worked example from internal/score: +40/-10 is 45 churn, which is inside
// "small" but below its anchor, so it is paid at 1.467x and scores 132 rather
// than the 135 a flat tier would give.
func TestScorePreviewMatchesTheScorer(t *testing.T) {
	code, resp := preview(t, "additions=40&deletions=10&verdicts=APPROVED")
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	want := score.Compute(score.DefaultRules(), score.Input{
		Additions: 40, Deletions: 10, Verdict: store.VerdictApproved, Attempt: 1,
	})
	if resp.Total != want.Score || resp.Rounds[0].Score != want.Score {
		t.Errorf("total = %d, rounds = %+v, want %d", resp.Total, resp.Rounds, want.Score)
	}
	if resp.Bucket != want.Bucket {
		t.Errorf("bucket = %q, want %q", resp.Bucket, want.Bucket)
	}
	if resp.Churn != 45 {
		t.Errorf("churn = %v, want 45: deletions weighed at the configured 0.5", resp.Churn)
	}
	if resp.Rate < 1.46 || resp.Rate > 1.47 {
		t.Errorf("rate = %v, want the interpolated ~1.467x rather than the tier's 1.5x", resp.Rate)
	}
}

// The reason rounds are shown at all: the decay is what makes getting it right
// first time worth more than getting there eventually, and a calculator that
// only scored one review would hide it.
func TestScorePreviewDecaysAcrossRounds(t *testing.T) {
	_, slow := preview(t, "additions=40&deletions=10&verdicts=COMMENTED,COMMENTED,APPROVED")
	_, quick := preview(t, "additions=40&deletions=10&verdicts=APPROVED")

	if len(slow.Rounds) != 3 {
		t.Fatalf("rounds = %+v, want 3", slow.Rounds)
	}
	for i, r := range slow.Rounds {
		if r.Attempt != i+1 {
			t.Errorf("round %d has attempt %d", i, r.Attempt)
		}
	}
	if slow.Rounds[2].Score >= quick.Total {
		t.Errorf("approving at round 3 scored %d, first pass %d: the decay must bite", slow.Rounds[2].Score, quick.Total)
	}
	if slow.Total >= quick.Total {
		t.Errorf("three rounds totalled %d, first-pass approval %d: comments must not pay better", slow.Total, quick.Total)
	}
}

// An empty sequence is the question people arrive with ("what would this be
// worth?"), so it answers for a clean first-pass approval rather than 400ing.
func TestScorePreviewDefaultsToAFirstPassApproval(t *testing.T) {
	_, resp := preview(t, "additions=40&deletions=10")
	if len(resp.Rounds) != 1 || resp.Rounds[0].Verdict != store.VerdictApproved {
		t.Errorf("rounds = %+v, want one APPROVED round", resp.Rounds)
	}
}

// Outcomes of our own machinery are not feedback to an author and never score.
// Offering to price one would teach the wrong model of what earns points.
func TestScorePreviewRejectsNonsense(t *testing.T) {
	for _, q := range []string{
		"additions=SKIPPED",
		"additions=-5",
		"deletions=99999999",
		"verdicts=SKIPPED",
		"verdicts=APPROVED,ERROR",
		"verdicts=A,A,A,A,A,A,A,A,A,A,A",
	} {
		if code, _ := preview(t, q); code != http.StatusBadRequest {
			t.Errorf("%q: code = %d, want 400", q, code)
		}
	}
}

// A repo scored under its own rules must preview under those rules, or the
// calculator quietly answers for a policy that repo does not use.
func TestScorePreviewHonoursARepoOverride(t *testing.T) {
	half := 0.5
	s := testServer(withConfig(config.Config{Scoring: config.ScoringSettings{
		Repos: map[string]config.ScoringSettings{"o/thrifty": {Base: &half}},
	}}))
	code, scoped := serveJSON[scorePreviewResp](t, s.handleScorePreview, http.MethodGet,
		"/api/score/preview?additions=40&deletions=10&repo=o/thrifty", "")
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	_, global := preview(t, "additions=40&deletions=10")
	if scoped.Total >= global.Total {
		t.Errorf("repo total = %d, global = %d: the override must apply", scoped.Total, global.Total)
	}
}
