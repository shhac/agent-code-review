package dashboard

import (
	"context"
	"net/http"

	"github.com/shhac/agent-code-review/internal/store"
)

type reviewsResp struct {
	Reviews []store.Review `json:"reviews"`
}

type runsResp struct {
	Runs []store.Run `json:"runs"`
}

func (s *Server) handleReviews(w http.ResponseWriter, r *http.Request) {
	serveGet(s, w, r, func(ctx context.Context) (reviewsResp, error) {
		reviews, err := s.store.ListReviews(ctx, queryInt(r, "limit", 50, 500))
		return reviewsResp{Reviews: reviews}, err
	})
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	serveGet(s, w, r, func(ctx context.Context) (runsResp, error) {
		runs, err := s.store.ListRuns(ctx, queryInt(r, "limit", 20, 200))
		return runsResp{Runs: runs}, err
	})
}
