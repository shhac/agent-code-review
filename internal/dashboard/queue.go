package dashboard

// This file is the queue write surface: add (URL or repo/number, gated to
// watched repos) and reorder. Kept apart from the thin read handlers — this
// is the one part of the dashboard that validates untrusted input and
// mutates state.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/prref"
	"github.com/shhac/agent-code-review/internal/store"
)

// handleQueue lists on GET, adds a PR on POST, and removes one on DELETE —
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

// removeFromQueue drops a candidate entirely — the "changed our mind" path.
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

// queueView is a Candidate plus the display status the frontend keys its
// badges on. The store has no status column anymore — "reviewing" is derived
// from a live claim, "held" from the eligibility hold; everything else in
// the queue is "queued".
type queueView struct {
	store.Candidate
	Status string `json:"status"` // queued|reviewing|held
}

// claimStatus maps the shared predicates (store.Candidate.ClaimActive and
// .Held) to the dashboard's status vocabulary: a live claim is "reviewing",
// an eligibility hold is "held", anything else — including a stale claim the
// next cycle will reclaim — is "queued". The queue badges and the review-log
// header both derive from this one helper so they cannot disagree on the
// lease boundary.
func claimStatus(c store.Candidate, now time.Time, staleAfter time.Duration) string {
	if c.ClaimActive(now, staleAfter) {
		return "reviewing"
	}
	if c.Held(now) {
		return "held"
	}
	return "queued"
}

// viewQueue derives each candidate's display status. Pure — unit-tested.
func viewQueue(candidates []store.Candidate, now time.Time, staleAfter time.Duration) []queueView {
	out := make([]queueView, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, queueView{Candidate: c, Status: claimStatus(c, now, staleAfter)})
	}
	return out
}

// queueCounts is the fixed header-badge shape: waiting vs in-flight vs on
// hold, always summing to Total. A typed struct so a future status can't
// silently create a key nobody reads.
type queueCounts struct {
	Total     int `json:"total"`
	Queued    int `json:"queued"`
	Reviewing int `json:"reviewing"`
	Held      int `json:"held"`
}

type queueResp struct {
	Candidates []queueView `json:"candidates"`
	Counts     queueCounts `json:"counts"`
}

type queueAddResp struct {
	Queued bool   `json:"queued"`
	Title  string `json:"title"`
	Author string `json:"author"`
}

type queueRemoveResp struct {
	Removed bool `json:"removed"`
}

type queuePromoteResp struct {
	Promoted bool `json:"promoted"`
}

type queueReorderResp struct {
	Reordered bool `json:"reordered"`
}

// countQueue tallies views by display status. Pure — unit-tested with
// viewQueue so the badge counts and per-row statuses cannot disagree.
func countQueue(views []queueView) queueCounts {
	counts := queueCounts{Total: len(views)}
	for _, v := range views {
		switch v.Status {
		case "queued":
			counts.Queued++
		case "reviewing":
			counts.Reviewing++
		case "held":
			counts.Held++
		}
	}
	return counts
}

func (s *Server) listQueue(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()
	candidates, err := s.store.ListQueue(ctx, "")
	if err != nil {
		s.fail(w, err)
		return
	}
	views := viewQueue(candidates, time.Now(), s.config().LeaseWindow())
	writeJSON(w, http.StatusOK, queueResp{Candidates: views, Counts: countQueue(views)})
}

// addToQueue accepts {"url": "<PR reference>"} — a full GitHub PR URL or the
// bare "owner/repo/pull/N" form — or {"repo": "owner/name", "number": N}.
// Either way the repo must be one of the configured watched repos — the
// dashboard is the surface other people use, so it only takes PRs this tool is
// actually set up to review.
func (s *Server) addToQueue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		prRef
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.URL != "" {
		ref, ok := parsePRRef(req.URL)
		if !ok {
			httpError(w, http.StatusBadRequest, "not a PR reference — expected https://github.com/owner/repo/pull/N or owner/repo/pull/N")
			return
		}
		req.prRef = ref
	}
	if !config.ValidRepoName(req.Repo) || req.Number <= 0 {
		httpError(w, http.StatusBadRequest, `need {"url": "owner/repo/pull/N"} or {"repo": "owner/name", "number": N}`)
		return
	}
	if !s.repoWatched(req.Repo) {
		httpError(w, http.StatusForbidden, req.Repo+" is not a watched repo — see the Config page for the allowed list")
		return
	}
	// Fetching metadata involves a gh round-trip; give it room.
	ctx, cancel := reqCtx(r, 30*time.Second)
	defer cancel()

	// Fetch real metadata up front (title/author/SHA) and reject closed or
	// merged PRs — discovery only backfills PRs that match the candidate
	// rules, which a manual add may not.
	c, err := s.manualCandidate(ctx, req.Repo, req.Number)
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

// repoWatched defers to the config-layer predicate — one definition of
// watch-list membership.
func (s *Server) repoWatched(repo string) bool {
	return s.config().WatchesRepo(repo)
}

// prRef is the queue-row wire shape shared by the remove, add, promote, and
// reorder request bodies (and the reorder validator's set key).
type prRef = prref.Ref

func decodePRRef(r *http.Request) (prRef, bool) {
	var req prRef
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return prRef{}, false
	}
	return req, req.Valid()
}

// parsePRRef accepts GitHub PR URLs and the bare owner/repo/pull/N form,
// delegating owner/name validation to config.ValidRepoName.
func parsePRRef(raw string) (prRef, bool) {
	return prref.ParseGitHubPull(raw)
}

// handleQueuePromote is the explicit "review this now" action: float the row
// to the top, clear any eligibility hold, and escalate it to a manual add
// (bypassing the pre-review candidacy recheck) — the same semantics as
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
// Rows under a live review claim are pinned — they cannot be reordered, and
// the request must not mention them.
func (s *Server) handleQueueReorder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req struct {
		Order []prRef `json:"order"`
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
// that are mid-review (their position is pinned while claimed). Pure —
// unit-tested directly.
func validateReorder(queue []store.Candidate, order []prRef, now time.Time, staleAfter time.Duration) error {
	reorderable := make(map[prRef]struct{}, len(queue))
	for _, c := range queue {
		if !c.ClaimActive(now, staleAfter) {
			reorderable[prRef{Repo: c.Repo, Number: c.Number}] = struct{}{}
		}
	}
	if len(order) != len(reorderable) {
		return fmt.Errorf("order lists %d PRs but %d are reorderable; it must cover every queued PR exactly once", len(order), len(reorderable))
	}
	seen := make(map[prRef]struct{}, len(order))
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
