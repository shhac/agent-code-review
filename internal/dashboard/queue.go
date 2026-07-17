package dashboard

// This file is the queue write surface: add (URL or repo/number, gated to
// watched repos), remove, promote, and reorder. Kept apart from the read
// side (queueview.go): this is the one part of the dashboard that validates
// untrusted input and mutates state.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/shhac/agent-code-review/internal/prref"
	"github.com/shhac/agent-code-review/internal/store"
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
	req, ok := decodePRRef(r)
	if !ok {
		httpError(w, http.StatusBadRequest, `need {"repo": "owner/name", "number": N}`)
		return
	}
	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()
	if err := s.store.Dequeue(ctx, req.Repo, req.Number); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, queueRemoveResp{Removed: true})
}

// addToQueue accepts {"url": "<PR reference>"}: a full GitHub PR URL or the
// bare "owner/repo/pull/N" form (which also covers "I know the repo and
// number"). One wire shape for the dashboard's only non-trivial untrusted
// input; the repo must be one of the configured watched repos, because the
// dashboard is the surface other people use, so it only takes PRs this tool
// is actually set up to review.
func (s *Server) addToQueue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.URL == "" {
		httpError(w, http.StatusBadRequest, `need {"url": "https://github.com/owner/repo/pull/N" or "owner/repo/pull/N"}`)
		return
	}
	ref, ok := prref.ParseGitHubPull(req.URL)
	if !ok {
		httpError(w, http.StatusBadRequest, "not a PR reference: expected https://github.com/owner/repo/pull/N or owner/repo/pull/N")
		return
	}
	if !s.config().WatchesRepo(ref.Repo) {
		httpError(w, http.StatusForbidden, ref.Repo+" is not a watched repo; see the Config page for the allowed list")
		return
	}
	// Fetching metadata involves a gh round-trip; give it room.
	ctx, cancel := reqCtx(r, 30*time.Second)
	defer cancel()

	// Fetch real metadata up front (title/author/SHA) and reject closed or
	// merged PRs; discovery only backfills PRs that match the candidate
	// rules, which a manual add may not.
	c, err := s.manualCandidate(ctx, ref.Repo, ref.Number)
	if err != nil {
		httpError(w, http.StatusBadGateway, err.Error())
		return
	}
	// Completed/skipped PRs are absent from the queue, so a manual re-add is
	// a plain enqueue; if it's already queued this just refreshes metadata.
	if err := s.store.Enqueue(ctx, c); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, queueAddResp{Queued: true, Title: c.Title, Author: c.Author})
}

// decodePRRef decodes the queue-row wire shape shared by the remove, add,
// promote, and reorder request bodies.
func decodePRRef(r *http.Request) (prref.Ref, bool) {
	var req prref.Ref
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return prref.Ref{}, false
	}
	return req, req.Valid()
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
	req, ok := decodePRRef(r)
	if !ok {
		httpError(w, http.StatusBadRequest, `need {"repo": "owner/name", "number": N}`)
		return
	}
	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()
	if err := s.store.Promote(ctx, req.Repo, req.Number); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, queuePromoteResp{Promoted: true})
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
	var req struct {
		Order []prref.Ref `json:"order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Order) == 0 {
		httpError(w, http.StatusBadRequest, `need {"order": [{"repo", "number"}, ...]} covering every queued PR`)
		return
	}
	ctx, cancel := reqCtx(r, 30*time.Second)
	defer cancel()

	queue, err := s.store.ListQueue(ctx, "")
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := validateReorder(queue, req.Order, time.Now(), s.config().LeaseWindow()); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	positions := make([]store.QueuePosition, 0, len(req.Order))
	for pos, ref := range req.Order {
		positions = append(positions, store.QueuePosition{Repo: ref.Repo, Number: ref.Number, Position: pos + 1})
	}
	if err := s.store.Reorder(ctx, positions); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, queueReorderResp{Reordered: true})
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
