// Steering: an instruction from the PR's author that shapes the next review of
// their PR. The authorisation rule is the reason this endpoint exists at all,
// so it lives here beside the handler rather than in a middleware that reads
// as a formality.

package dashboard

import (
	"context"
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
// The remaining rungs (permission, then the claim) are steeringRefusal's, so
// that the queue-add path answers them identically. Permission before claim, so
// a 403 is returned to a stranger rather than "a review is running on this PR",
// which they have no business learning.
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
func (s *Server) steerableRow(ctx context.Context, r *http.Request, repo string, number int, msg string) (store.Candidate, viewer, *apiErr) {
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
	if bad := steeringRefusal(v, c.Author, msg, s.claimIsLive(c)); bad != nil {
		return store.Candidate{}, viewer{}, bad
	}
	return c, v, nil
}

// claimIsLive reports whether a row is under a live lease right now. One
// place reads the clock and the lease window for the steering paths, so the
// two cannot end up asking the question with different arguments.
func (s *Server) claimIsLive(c store.Candidate) bool {
	return c.ClaimActive(time.Now(), s.config().LeaseWindow())
}

// reviewInFlightMsg is the refusal a running review earns, worded for the
// author reading it: what is happening, and what to do instead. Named for the
// sentence it is, so that it cannot be confused with the predicate that decides
// when to use it.
const reviewInFlightMsg = "a review of this PR is running; its instructions are already fixed. " +
	"Steer it again once this review finishes."

// parseSteeringReq decodes the body: which PR, and the message (trimmed) if
// there is one. It needs no Server, so the wire contract is table-testable on
// its own. It does not judge the message — the hold endpoint shares this parser
// and has no message to judge, and message rules belong with the other rungs
// in steeringRefusal rather than split across two places.
func parseSteeringReq(w http.ResponseWriter, r *http.Request) (steeringReq, error) {
	req, err := decodeBody[steeringReq](w, r)
	if err != nil || req.Repo == "" || req.Number <= 0 {
		return steeringReq{}, &apiErr{http.StatusBadRequest,
			`need {"repo": "owner/name", "number": N, "message": "..."}`}
	}
	req.Message = strings.TrimSpace(req.Message)
	return req, nil
}

// steeringRefusal is the half of the ladder that both write paths share: the
// rungs about the MESSAGE and the ROW, given a caller already identified and a
// row already found. Returns nil when the message may be applied.
//
// It exists because there are two ways to steer a PR — /api/steering, and a
// queue add carrying a message — and they have to answer identically. They did
// not: the add path was missing the claim rung entirely, which is the hole
// 159548d closed by hand. Copying a rung across is what this replaces.
//
// The caller decides what to DO with a refusal, which is the one thing the two
// paths genuinely differ on. /api/steering returns the status; the add path
// renders only the sentence and still performs the add, because a caller who
// asked for two things is entitled to the one they may have.
func steeringRefusal(v viewer, author, msg string, claimed bool) *apiErr {
	switch {
	case len(msg) > store.SteeringMaxLen:
		return &apiErr{http.StatusBadRequest, "message is longer than the steering limit"}
	case !v.maySteer(author):
		return &apiErr{http.StatusForbidden, cannotSteer(author)}
	case claimed:
		return &apiErr{http.StatusConflict, reviewInFlightMsg}
	}
	return nil
}

// handleSteering sets or clears the steering for one PR. POST with a message
// sets it; POST with an empty message clears it.
func (s *Server) handleSteering(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	serveWrite(s, w, r, 10*time.Second, parseSteeringReq, func(ctx context.Context, req steeringReq) (steeringResp, error) {
		_, v, bad := s.steerableRow(ctx, r, req.Repo, req.Number, req.Message)
		if bad != nil {
			return steeringResp{}, bad
		}
		if req.Message == "" {
			if err := s.store.ClearSteering(ctx, req.Repo, req.Number); err != nil {
				return steeringResp{}, err
			}
			if err := s.releaseEditing(ctx, req.Repo, req.Number); err != nil {
				return steeringResp{}, err
			}
			return steeringResp{Cleared: true}, nil
		}
		st := store.Steering{Message: req.Message, SetBy: v.Handle, SetAt: time.Now()}
		if err := s.store.SetSteering(ctx, req.Repo, req.Number, st); err != nil {
			return steeringResp{}, err
		}
		// Saving IS being done editing, so the hold goes now rather than
		// lingering for the rest of its window: the whole point was to protect
		// the writing, and the writing is over.
		if err := s.releaseEditing(ctx, req.Repo, req.Number); err != nil {
			return steeringResp{}, err
		}
		return steeringResp{Steering: &st}, nil
	})
}
