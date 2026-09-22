// The steering hold: while an author has the steering editor open, their PR is
// parked so a free dispatcher slot cannot claim it mid-sentence.

package dashboard

import (
	"context"
	"net/http"
	"time"

	"github.com/shhac/agent-code-review/internal/store"
)

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
	serveWrite(s, w, r, 10*time.Second, parseSteeringReq, func(ctx context.Context, req steeringReq) (steeringHoldResp, error) {
		cfg := s.config()
		c, _, bad := s.steerableRow(ctx, r, req.Repo, req.Number, "")
		if bad != nil {
			return steeringHoldResp{}, bad
		}

		if r.Method == http.MethodDelete {
			if err := s.releaseEditing(ctx, req.Repo, req.Number); err != nil {
				return steeringHoldResp{}, err
			}
			return steeringHoldResp{State: holdReleased}, nil
		}

		window := cfg.SteeringHold()
		if window <= 0 {
			// Configured off: say so, rather than answering with the same empty
			// body a release gets. A client that cannot tell those apart keeps
			// renewing a hold this server is never going to take.
			return steeringHoldResp{State: holdDisabled}, nil
		}
		now := time.Now()
		since, capped := editingSession(c.Holds, now, cfg.SteeringHoldCap())
		if capped {
			// Past the cap. The standing hold is left to expire rather than
			// cleared: the author is still typing, and yanking the hold out
			// from under them early helps nobody.
			return steeringHoldResp{State: holdCapped}, nil
		}
		until := now.Add(window)
		patch := map[string]time.Time{store.HoldEditing: until}
		if since.IsZero() {
			patch[store.MarkEditingSince] = now
		}
		if err := s.store.SetHolds(ctx, req.Repo, req.Number, patch); err != nil {
			return steeringHoldResp{}, err
		}
		return steeringHoldResp{State: holdHeld, Until: &until}, nil
	})
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
