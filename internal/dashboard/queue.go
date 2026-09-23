package dashboard

// This file is the queue write surface: add (by PR URL/reference, gated to
// watched repos), remove, promote, and reorder. Kept apart from the read
// side (queueview.go): this is the one part of the dashboard that validates
// untrusted input and mutates state.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/shhac/crew-code-review/internal/prref"
	"github.com/shhac/crew-code-review/internal/store"
)

// handleQueue lists on GET, adds a PR on POST, and removes one on DELETE,
// mirroring `queue ls`/`queue add`/`queue rm` so users can manage their own
// PRs from the dashboard.
func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listQueue(w, r)
	case http.MethodPost:
		s.addToQueue(w, r)
	case http.MethodDelete:
		s.removeFromQueue(w, r)
	default:
		httpError(w, http.StatusMethodNotAllowed, "GET, POST, or DELETE")
	}
}

// removeFromQueue drops a candidate entirely: the "changed our mind" path.
func (s *Server) removeFromQueue(w http.ResponseWriter, r *http.Request) {
	serveWrite(s, w, r, 10*time.Second, decodePRRef, func(ctx context.Context, req prref.Ref) (queueRemoveResp, error) {
		if err := s.store.Dequeue(ctx, req.Repo, req.Number); err != nil {
			return queueRemoveResp{}, err
		}
		return queueRemoveResp{Removed: true}, nil
	})
}

// handleQueuePreflight resolves a PR reference WITHOUT queueing it, so the UI
// can ask "who wrote this, and may I steer it" before committing to an add.
//
// It exists because there is no window afterwards. A manual add lands on an
// empty queue and a free dispatcher slot takes it within the idle poll, so an
// author who wants to steer their own PR has to say so at add time or not at
// all.
//
// Advisory only: the add re-resolves and re-checks. A caller cannot gain
// anything by lying to this endpoint, because nothing here is remembered.
func (s *Server) handleQueuePreflight(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	serveWrite(s, w, r, 30*time.Second, s.decodeWatchedPR, func(ctx context.Context, pr watchedPR) (queuePreflightResp, error) {
		c, err := s.fetchManual(ctx, pr.Ref)
		if err != nil {
			return queuePreflightResp{}, err
		}
		v, err := s.identify(ctx, r)
		if err != nil {
			return queuePreflightResp{}, err
		}
		// Through steeringRefusal, the same ladder the add itself will run.
		// Asking only maySteer here meant preflight answered a narrower question
		// than the endpoint it previews: a PR already under review passed the
		// permission rung, so the editor opened and offered to steer, and the
		// add then refused with "a review of this PR is running". Advisory is
		// not licence to promise something the next call will decline.
		//
		// The message is empty at this point, so the length rung cannot fire;
		// that is the one rung preflight genuinely cannot answer in advance.
		claimed, err := s.claimedNow(ctx, pr.Repo, pr.Number)
		if err != nil {
			return queuePreflightResp{}, err
		}
		resp := queuePreflightResp{
			Repo: c.Repo, Number: c.Number, Title: c.Title, Author: c.Author,
			MaySteer: true,
		}
		if bad := steeringRefusal(v, c.Author, "", claimed); bad != nil {
			resp.MaySteer, resp.Refusal = false, bad.msg
		}
		return resp, nil
	})
}

// addReq is the add/preflight wire shape: a full GitHub PR URL or the bare
// "owner/repo/pull/N" form, plus an optional steering message that only add
// reads. One shape for the dashboard's only non-trivial untrusted input.
type addReq struct {
	URL      string `json:"url"`
	Steering string `json:"steering"`
}

// watchedPR is an add/preflight body that decodeWatchedPR accepted: the parsed
// reference, and the steering message only add reads.
type watchedPR struct {
	prref.Ref
	Steering string
}

// decodeWatchedPR is the shared front half of add and preflight: decode the
// body, parse the reference, and refuse a repo this tool is not set up to
// review. The dashboard is the surface other people use, so the watched-repo
// check is not optional, and having one function do it means add and preflight
// cannot come to different conclusions about what is acceptable.
func (s *Server) decodeWatchedPR(w http.ResponseWriter, r *http.Request) (watchedPR, error) {
	req, err := decodeBody[addReq](w, r)
	if err != nil || req.URL == "" {
		return watchedPR{}, &apiErr{http.StatusBadRequest,
			`need {"url": "https://github.com/owner/repo/pull/N" or "owner/repo/pull/N"}`}
	}
	ref, ok := prref.ParseGitHubPull(req.URL)
	if !ok {
		return watchedPR{}, &apiErr{http.StatusBadRequest,
			"not a PR reference: expected https://github.com/owner/repo/pull/N or owner/repo/pull/N"}
	}
	if !s.config().WatchesRepo(ref.Repo) {
		return watchedPR{}, &apiErr{http.StatusForbidden,
			ref.Repo + " is not a watched repo; see the Config page for the allowed list"}
	}
	return watchedPR{Ref: ref, Steering: req.Steering}, nil
}

// fetchManual resolves a PR's live metadata for add and preflight. A failure
// is GitHub's (or gh's), not ours and not the caller's, hence the 502.
func (s *Server) fetchManual(ctx context.Context, ref prref.Ref) (store.Candidate, error) {
	c, err := s.manualCandidate(ctx, ref.Repo, ref.Number)
	if err != nil {
		return store.Candidate{}, &apiErr{http.StatusBadGateway, err.Error()}
	}
	return c, nil
}

// claimedNow reports whether this PR is queued AND under a live claim. Not
// queued is not in flight: the row is gone, so a fresh add is exactly the clean
// path an author is told to take once a review finishes.
func (s *Server) claimedNow(ctx context.Context, repo string, number int) (bool, error) {
	c, ok, err := s.store.QueuedPR(ctx, repo, number)
	if err != nil || !ok {
		return false, err
	}
	return s.claimIsLive(c), nil
}

// addToQueue queues a PR, optionally with a steering message.
func (s *Server) addToQueue(w http.ResponseWriter, r *http.Request) {
	// Fetching metadata involves a gh round-trip; give it room.
	serveWrite(s, w, r, 30*time.Second, s.decodeWatchedPR, func(ctx context.Context, pr watchedPR) (queueAddResp, error) {
		// Fetch real metadata up front (title/author/SHA) and reject closed or
		// merged PRs; discovery only backfills PRs that match the candidate
		// rules, which a manual add may not.
		c, err := s.fetchManual(ctx, pr.Ref)
		if err != nil {
			return queueAddResp{}, err
		}
		// Steering rides in on the same write. Two writes would leave a window
		// where a free dispatcher slot claims the row before the instruction
		// lands, which on an empty queue is the normal case.
		st, refused, err := s.steeringForAdd(ctx, r, pr.Ref, c.Author, pr.Steering)
		if err != nil {
			return queueAddResp{}, err
		}
		c.Steering = st

		// Completed/skipped PRs are absent from the queue, so a manual re-add
		// is a plain enqueue; if it's already queued this just refreshes
		// metadata.
		if err := s.store.Enqueue(ctx, c); err != nil {
			return queueAddResp{}, err
		}
		return queueAddResp{
			Queued: true, Title: c.Title, Author: c.Author,
			Steered: c.Steering != nil, SteeringRefused: refused,
		}, nil
	})
}

// steeringForAdd decides what steering, if any, a manual add may carry: the
// steering to write, or the sentence explaining why the message was dropped.
// Both are empty when no message was sent.
//
// Authorisation is decided HERE, against the author gh just reported, not
// against anything the request claimed and not on the strength of the
// preflight. A refusal is not an error: the add still happens, because the
// caller asked for two things and is entitled to the one they may have.
func (s *Server) steeringForAdd(ctx context.Context, r *http.Request, ref prref.Ref, author, msg string) (st *store.Steering, refused string, err error) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return nil, "", nil
	}
	v, err := s.identify(ctx, r)
	if err != nil {
		return nil, "", err
	}
	// An add of a PR already queued is an upsert, so this path can write
	// steering onto a row that is under review right now — the one thing
	// /api/steering refuses. That is why the rungs are shared rather than
	// restated: the claim rung was once missing here, and a message accepted
	// then was discarded by the completion that retires the row, having
	// reported success.
	claimed, err := s.claimedNow(ctx, ref.Repo, ref.Number)
	if err != nil {
		return nil, "", err
	}
	if bad := steeringRefusal(v, author, msg, claimed); bad != nil {
		// Only the sentence, never the status: the add itself succeeds.
		return nil, bad.msg, nil
	}
	return &store.Steering{Message: msg, SetBy: v.Handle, SetAt: time.Now()}, "", nil
}

// decodePRRef decodes the queue-row wire shape shared by the remove and
// promote request bodies (add is url-only; reorder sends a list of them).
func decodePRRef(w http.ResponseWriter, r *http.Request) (prref.Ref, error) {
	req, err := decodeBody[prref.Ref](w, r)
	if err != nil || !req.Valid() {
		return prref.Ref{}, &apiErr{http.StatusBadRequest, `need {"repo": "owner/name", "number": N}`}
	}
	return req, nil
}

// handleQueuePromote is the explicit "review this now" action: float the row
// to the top, clear any eligibility hold, and escalate it to a manual add
// (bypassing the pre-review candidacy recheck), the same semantics as
// `queue promote`. Deliberately distinct from reorder: a drag changes only
// positions and never lifts a hold.
func (s *Server) handleQueuePromote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	serveWrite(s, w, r, 10*time.Second, decodePRRef, func(ctx context.Context, req prref.Ref) (queuePromoteResp, error) {
		if err := s.store.Promote(ctx, req.Repo, req.Number); err != nil {
			return queuePromoteResp{}, err
		}
		return queuePromoteResp{Promoted: true}, nil
	})
}

// reorderReq is the complete new order of the reorderable rows.
type reorderReq struct {
	Order []prref.Ref `json:"order"`
}

func decodeReorder(w http.ResponseWriter, r *http.Request) (reorderReq, error) {
	req, err := decodeBody[reorderReq](w, r)
	if err != nil || len(req.Order) == 0 {
		return reorderReq{}, &apiErr{http.StatusBadRequest,
			`need {"order": [{"repo", "number"}, ...]} covering every queued PR`}
	}
	return req, nil
}

// handleQueueReorder replaces the queued ordering in one write: the drag-and-
// drop UI sends the complete new order of the reorderable (unclaimed) rows.
// Rows under a live review claim are pinned: they cannot be reordered, and
// the request must not mention them.
func (s *Server) handleQueueReorder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	serveWrite(s, w, r, 30*time.Second, decodeReorder, func(ctx context.Context, req reorderReq) (queueReorderResp, error) {
		queue, err := s.store.ListQueue(ctx, "")
		if err != nil {
			return queueReorderResp{}, err
		}
		if err := validateReorder(queue, req.Order, time.Now(), s.config().LeaseWindow()); err != nil {
			return queueReorderResp{}, &apiErr{http.StatusBadRequest, err.Error()}
		}
		positions := make([]store.QueuePosition, 0, len(req.Order))
		for pos, ref := range req.Order {
			positions = append(positions, store.QueuePosition{Repo: ref.Repo, Number: ref.Number, Position: pos + 1})
		}
		if err := s.store.Reorder(ctx, positions); err != nil {
			return queueReorderResp{}, err
		}
		return queueReorderResp{Reordered: true}, nil
	})
}

// validateReorder checks that order is exactly the set of reorderable rows:
// every unclaimed queue row once, no duplicates, no unknown PRs, and no rows
// that are mid-review (their position is pinned while claimed). Pure:
// unit-tested directly.
func validateReorder(queue []store.Candidate, order []prref.Ref, now time.Time, staleAfter time.Duration) error {
	reorderable := make(map[prref.Ref]struct{}, len(queue))
	for _, c := range queue {
		if !c.ClaimActive(now, staleAfter) {
			reorderable[prref.Ref{Repo: c.Repo, Number: c.Number}] = struct{}{}
		}
	}
	if len(order) != len(reorderable) {
		return fmt.Errorf("order lists %d PRs but %d are reorderable; it must cover every queued PR exactly once", len(order), len(reorderable))
	}
	seen := make(map[prref.Ref]struct{}, len(order))
	for _, ref := range order {
		if _, ok := reorderable[ref]; !ok {
			return fmt.Errorf("%s#%d is not reorderable (not queued, or currently being reviewed)", ref.Repo, ref.Number)
		}
		if _, dup := seen[ref]; dup {
			return fmt.Errorf("%s#%d appears twice in the order", ref.Repo, ref.Number)
		}
		seen[ref] = struct{}{}
	}
	return nil
}
