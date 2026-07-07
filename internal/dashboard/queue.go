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
	if err := s.store.RemoveCandidate(ctx, req.Repo, req.Number); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": true})
}

func (s *Server) listQueue(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()
	candidates, err := s.store.ListCandidates(ctx, store.Filter{})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": pendingOnly(candidates)})
}

// pendingOnly drops reviewed candidates from the queue view: a reviewed PR
// graduates to Recent reviews, and its candidate row stays in the store only
// as the dedupe/refresh ledger. Skipped and error rows stay visible — they
// have no review row, so the queue is the only place they can be seen.
func pendingOnly(candidates []store.Candidate) []store.Candidate {
	out := make([]store.Candidate, 0, len(candidates))
	for _, c := range candidates {
		if c.Status != store.StatusReviewed {
			out = append(out, c)
		}
	}
	return out
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
	// Requeue inserts new or flips an existing candidate back to queued,
	// preserving discovered metadata either way.
	if err := s.store.Requeue(ctx, c); err != nil {
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

	queued, err := s.store.ListCandidates(ctx, store.Filter{Status: store.StatusQueued})
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
