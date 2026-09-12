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

// The preview must be the scorer's own answer, not a lookalike, and it must
// hand back the two halves it is made of: a total that does not decompose is
// a number somebody has to take on faith.
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
	if resp.Changed != 50 || resp.NetRemoved != 0 {
		t.Errorf("changed = %d, net removed = %d; want 50 changed and nothing net removed", resp.Changed, resp.NetRemoved)
	}
	// The halves must add up to the total, or the page's explanation is not an
	// explanation of the number beside it.
	rules := score.DefaultRules()
	if resp.SizeReward != rules.SizeReward(50) || resp.RemovalReward != 0 {
		t.Errorf("components = %v + %v, want score's own", resp.SizeReward, resp.RemovalReward)
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

// Only GET. The shared read frame owns that check, and routing through it is
// what keeps this endpoint from answering a POST as cheerfully as a GET.
func TestScorePreviewIsReadOnly(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		code, _ := serveJSON[scorePreviewResp](t, previewServer().handleScorePreview,
			method, "/api/score/preview?additions=40", "")
		if code != http.StatusMethodNotAllowed {
			t.Errorf("%s: code = %d, want 405", method, code)
		}
	}
}

// A repo scored under its own rules must preview under those rules, or the
// calculator quietly answers for a policy that repo does not use.
func TestScorePreviewHonoursARepoOverride(t *testing.T) {
	half := 50.0
	s := testServer(withConfig(config.Config{Scoring: config.ScoringSettings{
		Repos: map[string]config.ScoringSettings{"o/thrifty": {SizePoints: &half}},
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
	// Echoed back, so a caller can see which policy answered rather than
	// assuming the one it asked about.
	if scoped.Repo != "o/thrifty" || global.Repo != "" {
		t.Errorf("repo = %q / %q, want the scope each answer used", scoped.Repo, global.Repo)
	}
}
