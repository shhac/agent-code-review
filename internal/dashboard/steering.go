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

// steeringHoldResp tells the editor what the server did, as one named state
// rather than a pair of optional fields the client has to recombine. The states
// are distinct actions for the client, which is why "released" and "disabled"
// stopped sharing an empty body: only one of them means stop asking.
type steeringHoldResp struct {
	State holdState  `json:"state"`
	Until *time.Time `json:"until,omitempty"` // set only for holdHeld
}

type holdState string

const (
	holdHeld     holdState = "held"     // parked until Until; keep renewing
	holdCapped   holdState = "capped"   // this session has run long enough; stop renewing, the standing hold expires on its own
	holdReleased holdState = "released" // the session is over
	holdDisabled holdState = "disabled" // candidates.steering_hold is 0s; stop asking
)

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
		writeJSON(w, http.StatusOK, steeringHoldResp{State: holdReleased})
		return
	}

	window := cfg.SteeringHold()
	if window <= 0 {
		// Configured off: say so, rather than answering with the same empty
		// body a release gets. A client that cannot tell those apart keeps
		// renewing a hold this server is never going to take.
		writeJSON(w, http.StatusOK, steeringHoldResp{State: holdDisabled})
		return
	}
	now := time.Now()
	since, capped := editingSession(c.Holds, now, cfg.SteeringHoldCap())
	if capped {
		// Past the cap. The standing hold is left to expire rather than
		// cleared: the author is still typing, and yanking the hold out from
		// under them early helps nobody.
		writeJSON(w, http.StatusOK, steeringHoldResp{State: holdCapped})
		return
	}
	until := now.Add(window)
	patch := map[string]time.Time{store.HoldEditing: until}
	if since.IsZero() {
		patch[store.MarkEditingSince] = now
	}
	if err := s.store.SetHolds(ctx, req.Repo, req.Number, patch); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, steeringHoldResp{State: holdHeld, Until: &until})
}

// editingSession reads how long the current steering-editor session has been
// running, and whether it has outlived the renewal cap.
//
// A session is only live while its HOLD is. An expired hold means whoever held
// it stopped talking, so any mark left behind dates an ABANDONED session and is
// treated as absent. That self-healing is not a nicety: a client that fails to
// release is the normal case this whole mechanism is designed around (a closed
// tab, a dropped network, a capped session that stopped renewing), and a mark
// that outlived its hold would otherwise cap every future session on that row
// instantly, silently leaving the PR unprotected for good.
//
// Pure, so the cap boundary is table-testable without a clock or a store.
func editingSession(holds map[string]time.Time, now time.Time, cap time.Duration) (since time.Time, capped bool) {
	if !holds[store.HoldEditing].After(now) {
		return time.Time{}, false
	}
	since = holds[store.MarkEditingSince]
	return since, !since.IsZero() && now.Sub(since) > cap
}

// releaseEditing ends the session: the hold and the mark dating it, in one
// statement. Leaving the mark behind would make the next session start life
// already counted against the cap.
func (s *Server) releaseEditing(ctx context.Context, repo string, number int) error {
	return s.store.ClearHolds(ctx, repo, number, store.EditingNames...)
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
