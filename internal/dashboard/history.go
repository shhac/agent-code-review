package dashboard

import (
	"net/http"
	"time"

	"github.com/shhac/agent-code-review/internal/store"
)

type reviewsResp struct {
	Reviews []store.Review `json:"reviews"`
}

type runsResp struct {
	Runs []store.Run `json:"runs"`
}

func (s *Server) handleReviews(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()
	reviews, err := s.store.ListReviews(ctx, queryInt(r, "limit", 50, 500))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, reviewsResp{Reviews: reviews})
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()
	runs, err := s.store.ListRuns(ctx, queryInt(r, "limit", 20, 200))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runsResp{Runs: runs})
}
