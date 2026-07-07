package dashboard

// This file is the queue write surface: add (URL or repo/number, gated to
// watched repos) and reorder. Kept apart from the thin read handlers — this
// is the one part of the dashboard that validates untrusted input and
// mutates state.

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/discover"
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
	var req struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !config.ValidRepoName(req.Repo) || req.Number <= 0 {
		httpError(w, http.StatusBadRequest, `need {"repo": "owner/name", "number": N}`)
		return
	}
	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()
	if err := s.store.Dequeue(ctx, req.Repo, req.Number); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": true})
}

// queueView is a Candidate plus the display status the frontend keys its
// badges on. The store has no status column anymore — "reviewing" is derived
// from a live claim; everything else in the queue is "queued".
type queueView struct {
	store.Candidate
	Status string `json:"status"` // queued|reviewing
}

// viewQueue derives display statuses from the shared lease predicate
// (store.Candidate.ClaimActive): a live claim renders "reviewing"; anything
// else — including a stale claim the next cycle will reclaim — is "queued".
// Pure — unit-tested.
func viewQueue(candidates []store.Candidate, now time.Time, staleAfter time.Duration) []queueView {
	out := make([]queueView, 0, len(candidates))
	for _, c := range candidates {
		status := "queued"
		if c.ClaimActive(now, staleAfter) {
			status = "reviewing"
		}
		out = append(out, queueView{Candidate: c, Status: status})
	}
	return out
}

func (s *Server) listQueue(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()
	candidates, err := s.store.ListQueue(ctx, "")
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": viewQueue(candidates, time.Now(), s.config().LeaseWindow())})
}

// prRefPattern matches a PR reference in URL syntax, with or without the
// https://github.com/ prefix: "owner/repo/pull/123" works bare. Its repo
// segment mirrors config.ValidRepoName's grammar.
var prRefPattern = regexp.MustCompile(`^(?:https://github\.com/)?([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/pull/([0-9]+)`)

// addToQueue accepts {"url": "<PR reference>"} — a full GitHub PR URL or the
// bare "owner/repo/pull/N" form — or {"repo": "owner/name", "number": N}.
// Either way the repo must be one of the configured watched repos — the
// dashboard is the surface other people use, so it only takes PRs this tool is
// actually set up to review.
func (s *Server) addToQueue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
		URL    string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.URL != "" {
		m := prRefPattern.FindStringSubmatch(strings.TrimSpace(req.URL))
		if m == nil {
			httpError(w, http.StatusBadRequest, "not a PR reference — expected https://github.com/owner/repo/pull/N or owner/repo/pull/N")
			return
		}
		req.Repo = m[1]
		req.Number, _ = strconv.Atoi(m[2])
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
	c, err := discover.ManualCandidate(ctx, req.Repo, req.Number)
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
	writeJSON(w, http.StatusOK, map[string]any{"queued": true, "title": c.Title, "author": c.Author})
}

// repoWatched defers to the config-layer predicate — one definition of
// watch-list membership.
func (s *Server) repoWatched(repo string) bool {
	return s.config().WatchesRepo(repo)
}

// handleQueueMove nudges a queued PR up or down one place. Positions are
// normalized to the current display order (1..N) on every move, so ties on the
// default 0 become explicit and the swap always takes effect.
func (s *Server) handleQueueMove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req struct {
		Repo      string `json:"repo"`
		Number    int    `json:"number"`
		Direction string `json:"direction"` // "up" | "down"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.Direction != "up" && req.Direction != "down") {
		httpError(w, http.StatusBadRequest, `need {"repo", "number", "direction": "up"|"down"}`)
		return
	}
	ctx, cancel := reqCtx(r, 30*time.Second)
	defer cancel()

	queued, err := s.store.ListQueue(ctx, "")
	if err != nil {
		s.fail(w, err)
		return
	}
	reordered, found := applyMove(queued, req.Repo, req.Number, req.Direction)
	if !found {
		httpError(w, http.StatusNotFound, "PR is not in the queued list")
		return
	}
	for pos, c := range reordered {
		if err := s.store.SetQueuePos(ctx, c.Repo, c.Number, pos+1); err != nil {
			s.fail(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"moved": true})
}

// applyMove returns the queue with the target nudged one place up or down (a
// no-op at the edges), and whether the target was found. Pure — the boundary
// cases live in unit tests, not in production incidents.
func applyMove(queued []store.Candidate, repo string, number int, direction string) ([]store.Candidate, bool) {
	i := -1
	for idx, c := range queued {
		if c.Repo == repo && c.Number == number {
			i = idx
			break
		}
	}
	if i < 0 {
		return queued, false
	}
	j := i - 1
	if direction == "down" {
		j = i + 1
	}
	if j >= 0 && j < len(queued) {
		queued[i], queued[j] = queued[j], queued[i]
	}
	return queued, true
}
