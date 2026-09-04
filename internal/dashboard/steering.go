// Steering: an instruction from the PR's author that shapes the next review of
// their PR. The authorisation rule is the reason this endpoint exists at all,
// so it lives here beside the handler rather than in a middleware that reads
// as a formality.

package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/shhac/agent-code-review/internal/store"
)

type steeringReq struct {
	Repo    string `json:"repo"`
	Number  int    `json:"number"`
	Message string `json:"message"`
}

type steeringResp struct {
	Steering *store.Steering `json:"steering,omitempty"`
	Cleared  bool            `json:"cleared,omitempty"`
}

// viewerResp tells the UI who it is talking to, so it can show the steering
// box only where it would be accepted rather than offering it and failing.
type viewerResp struct {
	Login       string `json:"login,omitempty"`
	Handle      string `json:"handle,omitempty"`
	IsGHUser    bool   `json:"is_gh_user"`
	Anonymous   bool   `json:"anonymous"`
	MaxMessage  int    `json:"max_message"`
	SteerAnyPR  bool   `json:"steer_any_pr"`
	Explanation string `json:"explanation,omitempty"`
}

func (s *Server) handleViewer(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r, 5*time.Second)
	defer cancel()
	v, err := s.identify(ctx, r)
	if err != nil {
		s.fail(w, err)
		return
	}
	resp := viewerResp{
		Login: v.Login, Handle: v.Handle, IsGHUser: v.IsGH,
		Anonymous: v.anonymous(), MaxMessage: store.SteeringMaxLen, SteerAnyPR: v.IsGH,
	}
	switch {
	case v.anonymous():
		resp.Explanation = "not identified: the dashboard reads the identity `tailscale serve` attaches, so a direct connection or a tagged device is anonymous"
	case v.Handle == "":
		resp.Explanation = "identified as " + v.Login + ", which no roster row claims, so no PR is yours to steer"
	case v.IsGH:
		resp.Explanation = "reviews are posted as @" + v.Handle + ", so any PR here can be steered"
	default:
		resp.Explanation = "you may steer PRs authored by @" + v.Handle
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSteering sets or clears the steering for one PR. POST with a message
// sets it; POST with an empty message clears it.
//
// Authorisation is checked against the QUEUED candidate's author rather than
// anything the caller sent: the request names a PR, and who wrote that PR is a
// fact the store already holds. A caller cannot widen their own rights by
// describing the PR differently.
func (s *Server) handleSteering(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req steeringReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Repo == "" || req.Number <= 0 {
		httpError(w, http.StatusBadRequest, `need {"repo": "owner/name", "number": N, "message": "..."}`)
		return
	}
	msg := strings.TrimSpace(req.Message)
	if len(msg) > store.SteeringMaxLen {
		httpError(w, http.StatusBadRequest, "message is longer than the steering limit")
		return
	}

	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()

	v, err := s.identify(ctx, r)
	if err != nil {
		s.fail(w, err)
		return
	}
	if v.anonymous() {
		httpError(w, http.StatusUnauthorized, "not identified: steering needs the identity `tailscale serve` attaches")
		return
	}

	author, ok, err := s.candidateAuthor(ctx, req.Repo, req.Number)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !ok {
		httpError(w, http.StatusNotFound, "that PR is not queued")
		return
	}
	// 403 rather than 404: the caller is authenticated and the PR exists, and
	// telling them plainly that it is not theirs is more useful than pretending
	// it is missing.
	if !v.maySteer(author) {
		httpError(w, http.StatusForbidden, "only @"+author+" (or the account reviews are posted as) can steer that PR")
		return
	}

	if msg == "" {
		if err := s.store.ClearSteering(ctx, req.Repo, req.Number); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, steeringResp{Cleared: true})
		return
	}
	st := store.Steering{Message: msg, SetBy: v.Handle, SetAt: time.Now()}
	if err := s.store.SetSteering(ctx, req.Repo, req.Number, st); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, steeringResp{Steering: &st})
}

// candidateAuthor returns the queued PR's author. Steering is only meaningful
// for work that is going to be reviewed, so an unqueued PR is a 404 rather
// than a row waiting for a review that may never come.
func (s *Server) candidateAuthor(ctx context.Context, repo string, number int) (string, bool, error) {
	queue, err := s.store.ListQueue(ctx, repo)
	if err != nil {
		return "", false, err
	}
	for _, c := range queue {
		if strings.EqualFold(c.Repo, repo) && c.Number == number {
			return c.Author, true, nil
		}
	}
	return "", false, nil
}
