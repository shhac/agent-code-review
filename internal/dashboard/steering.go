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

	"github.com/shhac/agent-code-review/internal/config"
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

// Error makes a refusal usable as a plain error, which is what lets a handler
// inside the serveGet frame return one. Without it the frame's only vocabulary
// was 500, so a caller's own mistake (an unparseable cursor) was reported as
// the server having broken.
func (e *apiErr) Error() string { return e.msg }

// steerableRow answers "may this request steer that PR, right now", in the
// order the answers must be given, and hands back the row it decided on.
//
// The ORDER is the security property, not just the outcomes. Identity is
// checked before existence, so an anonymous caller cannot use this endpoint as
// an oracle for what is queued; existence is checked before permission, so a
// 403 is only ever returned to someone who could already see the PR is there.
// Reordering these compiles fine and changes what the endpoint discloses,
// which is why they live together in one function with this comment rather
// than spread through a handler.
//
// The claim rung comes last because it is the only one that is about TIMING
// rather than about the caller. A review in flight built its prompt from the
// candidate the dispatcher pulled and never re-reads the row, so an edit
// landing now could not reach it, and completion would retire the row and the
// message with it. Accepting the write would return 200 for an instruction
// that goes nowhere, which is worse than refusing it.
//
// The author comes from the STORE. Nothing the request says about who wrote
// the PR is consulted.
func (s *Server) steerableRow(ctx context.Context, r *http.Request, repo string, number int, cfg config.Config) (store.Candidate, viewer, *apiErr) {
	v, err := s.identify(ctx, r)
	if err != nil {
		return store.Candidate{}, viewer{}, &apiErr{http.StatusInternalServerError, err.Error()}
	}
	if v.anonymous() {
		return store.Candidate{}, viewer{}, &apiErr{http.StatusUnauthorized,
			"not identified: steering needs the identity `tailscale serve` attaches"}
	}
	c, ok, err := s.store.QueuedPR(ctx, repo, number)
	if err != nil {
		return store.Candidate{}, viewer{}, &apiErr{http.StatusInternalServerError, err.Error()}
	}
	if !ok {
		return store.Candidate{}, viewer{}, &apiErr{http.StatusNotFound, "that PR is not queued"}
	}
	if !v.maySteer(c.Author) {
		// 403 rather than 404: the caller is identified and the PR exists, and
		// saying so plainly beats pretending it is missing.
		return store.Candidate{}, viewer{}, &apiErr{http.StatusForbidden, cannotSteer(c.Author)}
	}
	if c.ClaimActive(time.Now(), cfg.LeaseWindow()) {
		return store.Candidate{}, viewer{}, &apiErr{http.StatusConflict, reviewInFlight}
	}
	return c, v, nil
}

// reviewInFlight is the refusal a running review earns, worded for the author
// reading it: what is happening, and what to do instead.
const reviewInFlight = "a review of this PR is running; its instructions are already fixed. " +
	"Steer it again once this review finishes."

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

// steeringHoldResp tells the editor what the server did. Until is when the PR
// is parked to; Capped says renewal has stopped, so the client can say the PR
// is no longer held rather than silently believing it still is.
type steeringHoldResp struct {
	Until  *time.Time `json:"until,omitempty"`
	Capped bool       `json:"capped,omitempty"`
}

// handleSteeringHold parks a PR while its author has the steering editor open,
// and releases it when they are done.
//
// The client says WHICH PR and nothing else. It does not get to say for how
// long, because a caller that could name the duration could park its own PR
// indefinitely; the server takes that from candidates.steering_hold. Two
// independent bounds keep "briefly" honest:
//
//   - The hold EXPIRES on its own. A closed laptop, a crashed tab or a dropped
//     network releases nothing, so nothing may depend on a release arriving.
//   - Renewal STOPS at Config.SteeringHoldCap, measured from the start of the
//     editing session. The expiry bounds a client that stops talking; this
//     bounds one that never does, such as a modal left open over lunch.
//
// POST renews, DELETE releases.
func (s *Server) handleSteeringHold(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		httpError(w, http.StatusMethodNotAllowed, "POST or DELETE only")
		return
	}
	req, _, bad := parseSteeringReq(r)
	if bad != nil {
		httpError(w, bad.code, bad.msg)
		return
	}

	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()

	cfg := s.config()
	c, _, bad := s.steerableRow(ctx, r, req.Repo, req.Number, cfg)
	if bad != nil {
		httpError(w, bad.code, bad.msg)
		return
	}

	if r.Method == http.MethodDelete {
		if err := s.releaseEditing(ctx, req.Repo, req.Number); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, steeringHoldResp{})
		return
	}

	window := cfg.SteeringHold()
	if window <= 0 {
		// Configured off: answer plainly rather than holding for zero time,
		// which would read to the client as a hold that expired instantly.
		writeJSON(w, http.StatusOK, steeringHoldResp{})
		return
	}
	now := time.Now()
	since := c.EditingSince
	if since != nil && now.Sub(*since) > cfg.SteeringHoldCap() {
		// Past the cap. The standing hold is left to expire rather than
		// cleared: the author is still typing, and yanking the hold out from
		// under them early helps nobody.
		writeJSON(w, http.StatusOK, steeringHoldResp{Capped: true})
		return
	}
	if since == nil {
		if err := s.store.SetEditingSince(ctx, req.Repo, req.Number, &now); err != nil {
			s.fail(w, err)
			return
		}
	}
	until := now.Add(window)
	if err := s.store.SetHold(ctx, req.Repo, req.Number, store.HoldEditing, until); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, steeringHoldResp{Until: &until})
}

// releaseEditing lifts the editing hold and forgets the session. Both, always:
// leaving the anchor behind would make the next editing session start life
// already counted against the cap.
func (s *Server) releaseEditing(ctx context.Context, repo string, number int) error {
	if err := s.store.ClearHold(ctx, repo, number, store.HoldEditing); err != nil {
		return err
	}
	return s.store.SetEditingSince(ctx, repo, number, nil)
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

	_, v, bad := s.steerableRow(ctx, r, req.Repo, req.Number, s.config())
	if bad != nil {
		httpError(w, bad.code, bad.msg)
		return
	}

	if msg == "" {
		if err := s.store.ClearSteering(ctx, req.Repo, req.Number); err != nil {
			s.fail(w, err)
			return
		}
		if err := s.releaseEditing(ctx, req.Repo, req.Number); err != nil {
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
	// Saving IS being done editing, so the hold goes now rather than lingering
	// for the rest of its window: the whole point was to protect the writing,
	// and the writing is over.
	if err := s.releaseEditing(ctx, req.Repo, req.Number); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, steeringResp{Steering: &st})
}
