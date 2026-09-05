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
	// Total is how many history rows match q, across the whole table rather
	// than this page. Without it the page cannot tell "50 results" from "the
	// first 50 of 900", which is the bug this endpoint used to have in its
	// worst form: the browser filtered a fixed 500-row window and presented
	// the survivors as the complete answer.
	Total int `json:"total"`
	// NextCursor is what the page sends back as ?cursor= to advance. Opaque
	// to the browser: it names a row, so the page never has to compute an
	// offset that a review completing mid-read would invalidate.
	NextCursor string `json:"next_cursor,omitempty"`
}

// handleReviews serves one page of history, filtered and paged server-side.
//
// q is matched in SQL against every row, not against the page: searching is
// the whole reason the endpoint pages at all, so a filter that only saw the
// current page would be the browser-side bug moved one layer down.
//
// A bad cursor is a 400 rather than a silent first page. The browser only ever
// sends back a cursor this endpoint gave it, so a malformed one means the page
// and the server disagree about where the reader is, and showing page 1 while
// the pager reads "3/18" hides that.
func (s *Server) handleReviews(w http.ResponseWriter, r *http.Request) {
	serveGet(s, w, r, func(ctx context.Context) (reviewsResp, error) {
		text := r.URL.Query().Get("q")
		sort, err := store.ReviewSort(r.URL.Query().Get("sort")).Normalise()
		if err != nil {
			return reviewsResp{}, &apiErr{http.StatusBadRequest, err.Error()}
		}
		after, err := store.ParseReviewCursor(r.URL.Query().Get("cursor"), text, sort)
		if err != nil {
			return reviewsResp{}, &apiErr{http.StatusBadRequest, err.Error()}
		}
		page, err := s.store.SearchReviews(ctx, store.ReviewQuery{
			Text:  text,
			Sort:  sort,
			Limit: queryInt(r, "limit", 50, 500),
			After: after,
		})
		return reviewsResp{
			Reviews:    historyReviewsOf(page.Reviews),
			Total:      page.Total,
			NextCursor: page.NextCursor,
		}, err
	})
}
