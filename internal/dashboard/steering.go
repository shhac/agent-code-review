// Steering: an instruction from the PR's author that shapes the next review of
// their PR. The authorisation rule is the reason this endpoint exists at all,
// so it lives here beside the handler rather than in a middleware that reads
// as a formality.

package dashboard

import (
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

// viewerResp tells the UI who it is talking to. Deliberately narrow: the chip
// renders an identity, and whether a given PR is steerable is answered per row
// by queueView.MaySteer, so nothing here describes permissions.
//
// State is the classification, named once here rather than re-derived by the
// client from a combination of booleans. The English prose that used to ride
// along was assembled by a switch in Go and consumed only as a tooltip;
// wording belongs to the client.
type viewerResp struct {
	State  viewerState `json:"state"`
	Login  string      `json:"login,omitempty"`
	Handle string      `json:"handle,omitempty"`
}

// viewerState is the four ways the dashboard can know a caller.
type viewerState string

const (
	// viewerAnonymous: nothing was proved. Either the request did not come
	// through the tailscale proxy, or it is a tagged device or Funnel traffic,
	// for which Tailscale attaches no identity at all.
	viewerAnonymous viewerState = "anonymous"
	// viewerUnmapped: authenticated, but no roster row claims that login.
	viewerUnmapped viewerState = "unmapped"
	// viewerAuthor: a rostered person, who may steer their own PRs.
	viewerAuthor viewerState = "author"
	// viewerOperator: the account reviews are posted as, which may steer any.
	viewerOperator viewerState = "operator"
)

// state classifies a viewer. Pure, so the four cases are table-testable
// without building a request.
func (v viewer) state() viewerState {
	switch {
	case v.anonymous():
		return viewerAnonymous
	case v.Handle == "":
		return viewerUnmapped
	case v.IsGH:
		return viewerOperator
	default:
		return viewerAuthor
	}
}

func (s *Server) handleViewer(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r, 5*time.Second)
	defer cancel()
	v, err := s.identify(ctx, r)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, viewerResp{State: v.state(), Login: v.Login, Handle: v.Handle})
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

	// Steering is only meaningful for work that is going to be reviewed, and
	// the author it is checked against comes from the STORE: naming a
	// different author in the request cannot widen anyone's rights.
	c, ok, err := s.store.QueuedPR(ctx, req.Repo, req.Number)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !ok {
		httpError(w, http.StatusNotFound, "that PR is not queued")
		return
	}
	author := c.Author
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
