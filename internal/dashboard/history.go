package dashboard

import (
	"context"
	"net/http"
	"time"

	"github.com/shhac/agent-code-review/internal/store"
)

// historyReview is the review shape /api/reviews serves. Deliberately its own
// type rather than store.Review: serializing the persistence struct made every
// column public API by default, so a field added for internal bookkeeping
// shipped to the browser without anyone deciding it should. It also sent
// usage_raw, the verbatim per-invocation engine payload, for up to 500 rows a
// request, to a UI that never reads it.
//
// The same pattern reviewlog.go already uses for its own views.
type historyReview struct {
	Repo          string    `json:"repo"`
	Number        int       `json:"number"`
	LogKey        string    `json:"log_key,omitempty"`
	Title         string    `json:"title"`
	Author        string    `json:"author"`
	HeadSHA       string    `json:"head_sha"`
	Verdict       string    `json:"verdict"`
	Engine        string    `json:"engine"`
	Model         string    `json:"model,omitempty"`
	Effort        string    `json:"effort,omitempty"`
	EngineVersion string    `json:"engine_version,omitempty"`
	ReviewedAt    time.Time `json:"reviewed_at"`
	DurationSecs  int       `json:"duration_secs"`
	WorkDir       string    `json:"work_dir,omitempty"`
	TokensUsed    int       `json:"tokens_used"`
	// CostUSD is the run's spend however it is best known: the engine's own
	// figure when it reported one, otherwise ours. The raw column this used
	// to send reads as 0 for every codex review, because codex reports no
	// cost at all, so a history page of codex reviews showed a column of
	// zeroes that looked like "free" rather than "not reported".
	//
	// This is also the figure metrics.go and reviewlog.go already use, so
	// sending the raw one here was the endpoint disagreeing with the rest of
	// the dashboard about what a review cost.
	CostUSD float64 `json:"cost_usd"`
	// CostEstimated marks CostUSD as ours rather than the engine's, so the
	// page can say which it is showing instead of presenting an inference as
	// a measurement. Same pair reviewlog.go sends.
	CostEstimated bool `json:"cost_estimated,omitempty"`
}

func historyReviewsOf(reviews []store.Review) []historyReview {
	out := make([]historyReview, 0, len(reviews))
	for _, r := range reviews {
		out = append(out, historyReview{
			Repo: r.Repo, Number: r.Number, LogKey: r.LogKey, Title: r.Title,
			Author: r.Author, HeadSHA: r.HeadSHA, Verdict: r.Verdict,
			Engine: r.Engine, Model: r.Model, Effort: r.Effort,
			EngineVersion: r.EngineVersion, ReviewedAt: r.ReviewedAt,
			DurationSecs: r.DurationSecs, WorkDir: r.WorkDir,
			TokensUsed: r.TokensUsed,
			CostUSD:    r.EffectiveCostUSD(), CostEstimated: r.CostEstimated(),
		})
	}
	return out
}

type reviewsResp struct {
	Reviews []historyReview `json:"reviews"`
}

func (s *Server) handleReviews(w http.ResponseWriter, r *http.Request) {
	serveGet(s, w, r, func(ctx context.Context) (reviewsResp, error) {
		reviews, err := s.store.ListReviews(ctx, queryInt(r, "limit", 50, 500))
		return reviewsResp{Reviews: historyReviewsOf(reviews)}, err
	})
}
