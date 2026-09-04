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

// apiErr is a refusal with the status it should carry, so the authorisation
// ladder can be one function that returns "no, and here is the code" rather
// than a sequence of writes interleaved with transport concerns.
type apiErr struct {
	code int
	msg  string
}

// steeringActor answers "may this request steer that PR", in the order the
// answers must be given.
//
// The ORDER is the security property, not just the outcomes. Identity is
// checked before existence, so an anonymous caller cannot use this endpoint as
// an oracle for what is queued; existence is checked before permission, so a
// 403 is only ever returned to someone who could already see the PR is there.
// Reordering these compiles fine and changes what the endpoint discloses,
// which is why they live together in one function with this comment rather
// than spread through a handler.
//
// The author comes from the STORE. Nothing the request says about who wrote
// the PR is consulted.
func (s *Server) steeringActor(ctx context.Context, r *http.Request, repo string, number int) (viewer, *apiErr) {
	v, err := s.identify(ctx, r)
	if err != nil {
		return viewer{}, &apiErr{http.StatusInternalServerError, err.Error()}
	}
	if v.anonymous() {
		return viewer{}, &apiErr{http.StatusUnauthorized,
			"not identified: steering needs the identity `tailscale serve` attaches"}
	}
	c, ok, err := s.store.QueuedPR(ctx, repo, number)
	if err != nil {
		return viewer{}, &apiErr{http.StatusInternalServerError, err.Error()}
	}
	if !ok {
		return viewer{}, &apiErr{http.StatusNotFound, "that PR is not queued"}
	}
	if !v.maySteer(c.Author) {
		// 403 rather than 404: the caller is identified and the PR exists, and
		// saying so plainly beats pretending it is missing.
		return viewer{}, &apiErr{http.StatusForbidden, cannotSteer(c.Author)}
	}
	return v, nil
}

// parseSteeringReq decodes and validates the body. Pure over the reader, so
// the wire contract is table-testable without a Server.
func parseSteeringReq(r *http.Request) (steeringReq, string, *apiErr) {
	var req steeringReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Repo == "" || req.Number <= 0 {
		return req, "", &apiErr{http.StatusBadRequest,
			`need {"repo": "owner/name", "number": N, "message": "..."}`}
	}
	msg := strings.TrimSpace(req.Message)
	if len(msg) > store.SteeringMaxLen {
		return req, "", &apiErr{http.StatusBadRequest, "message is longer than the steering limit"}
	}
	return req, msg, nil
}

// handleSteering sets or clears the steering for one PR. POST with a message
// sets it; POST with an empty message clears it.
func (s *Server) handleSteering(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	req, msg, bad := parseSteeringReq(r)
	if bad != nil {
		httpError(w, bad.code, bad.msg)
		return
	}

	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()

	v, bad := s.steeringActor(ctx, r, req.Repo, req.Number)
	if bad != nil {
		httpError(w, bad.code, bad.msg)
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
