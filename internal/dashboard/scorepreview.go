package dashboard

// "What would my PR score?", answered by the scorer itself.
//
// The arithmetic is not re-implemented in the browser. Every dial the answer
// depends on already reaches the page (base, churn unit, deletion weight, the
// anchors, the verdict multipliers, the decay), so a client-side calculator
// was possible and would have been a second implementation of the whole
// policy: the exact thing shipping the anchors from Go was meant to stop. A
// preview that disagrees with the daemon is worse than no preview, because it
// is the number people plan against.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/shhac/agent-code-review/internal/score"
	"github.com/shhac/agent-code-review/internal/store"
)

// Bounds on a hypothetical. Generous enough for any real PR and small enough
// that a hand-edited query cannot ask for a million rounds of arithmetic.
const (
	maxPreviewLines  = 1_000_000
	maxPreviewRounds = 10
)

// scorePreviewResp is one hypothetical PR, round by round.
//
// Churn, Bucket and Rate travel with the scores because the score alone does
// not explain itself: the same line count scores very differently either side
// of a tier, and the rate is the part a reader cannot infer.
type scorePreviewResp struct {
	// Repo is which ruleset priced this, echoed back because it decides the
	// answer: a repo with its own scoring block is scored under that, and a
	// preview that quietly used the global one would promise points that repo
	// will never pay. Empty is the global policy.
	Repo      string `json:"repo"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	// DeletionWeight travels with the counts so the page can show the
	// ARITHMETIC rather than assert a total: "200 added plus 100 removed at
	// 1.5x" is the definition of churn stated in the one place somebody is
	// already asking where a number came from.
	DeletionWeight float64             `json:"deletion_weight"`
	Churn          float64             `json:"churn"`
	Bucket         string              `json:"bucket"`
	Rate           float64             `json:"rate"`
	Rounds         []scorePreviewRound `json:"rounds"`
	Total          int                 `json:"total"`
}

// scorePreviewRound is one review of that PR. Attempt is 1-based, matching
// what a stored score records.
type scorePreviewRound struct {
	Attempt int    `json:"attempt"`
	Verdict string `json:"verdict"`
	Score   int    `json:"score"`
}

// handleScorePreview scores a hypothetical PR: additions, deletions, and the
// sequence of verdicts it collects, as `verdicts=COMMENTED,APPROVED`.
//
// Every round is scored at the same size, because a preview is asking what a
// PR of THIS shape earns rather than replaying a history where the diff also
// grew. The decay across rounds is the whole point of showing them.
func (s *Server) handleScorePreview(w http.ResponseWriter, r *http.Request) {
	// Through the shared read frame, which is where the method check lives:
	// answering a POST as cheerfully as a GET is the bug that frame exists to
	// have fixed once.
	serveGet(s, w, r, func(context.Context) (scorePreviewResp, error) {
		return s.scorePreview(r.URL.Query())
	})
}

func (s *Server) scorePreview(q url.Values) (scorePreviewResp, error) {
	additions, err := previewLines(q.Get("additions"))
	if err != nil {
		return scorePreviewResp{}, &apiErr{http.StatusBadRequest, "additions: " + err.Error()}
	}
	deletions, err := previewLines(q.Get("deletions"))
	if err != nil {
		return scorePreviewResp{}, &apiErr{http.StatusBadRequest, "deletions: " + err.Error()}
	}
	verdicts, err := previewVerdicts(q.Get("verdicts"))
	if err != nil {
		return scorePreviewResp{}, &apiErr{http.StatusBadRequest, err.Error()}
	}

	repo := q.Get("repo")
	rules := s.config().ResolveScoring(repo)
	churn := rules.Churn(additions, deletions)
	resp := scorePreviewResp{
		Repo:           repo,
		Additions:      additions,
		Deletions:      deletions,
		DeletionWeight: rules.DeletionWeight,
		Churn:          churn,
		Rate:           rules.Rate(churn),
		Rounds:         make([]scorePreviewRound, 0, len(verdicts)),
	}
	for i, v := range verdicts {
		got := score.Compute(rules, score.Input{
			Additions: additions, Deletions: deletions, Verdict: v, Attempt: i + 1,
		})
		resp.Bucket = got.Bucket
		resp.Total += got.Score
		resp.Rounds = append(resp.Rounds, scorePreviewRound{Attempt: i + 1, Verdict: v, Score: got.Score})
	}
	return resp, nil
}

// previewLines parses a line count. Empty is 0, which is a legal PR half: a
// pure deletion has no additions.
func previewLines(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("must be a whole number, got %q", raw)
	}
	if n < 0 || n > maxPreviewLines {
		return 0, fmt.Errorf("must be between 0 and %d, got %d", maxPreviewLines, n)
	}
	return n, nil
}

// previewVerdicts parses the round sequence. Only verdicts that can actually
// be recorded are accepted: SKIPPED and ERROR are outcomes of our machinery,
// never feedback to an author, and offering to "score" one would teach the
// wrong model of what earns points.
func previewVerdicts(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return []string{store.VerdictApproved}, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > maxPreviewRounds {
		return nil, fmt.Errorf("verdicts: at most %d rounds, got %d", maxPreviewRounds, len(parts))
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		v := strings.ToUpper(strings.TrimSpace(p))
		if !store.IsRealVerdict(v) {
			return nil, fmt.Errorf("verdicts: %q is not a review outcome; use %s, %s or %s",
				p, store.VerdictApproved, store.VerdictCommented, store.VerdictRequestedChanges)
		}
		out = append(out, v)
	}
	return out, nil
}
