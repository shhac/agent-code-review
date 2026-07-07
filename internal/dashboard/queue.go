// Queue write surface: add (URL or repo/number, gated to watched repos) and
// reorder. Kept apart from the thin read handlers — this is the one part of
// the dashboard that validates untrusted input and mutates state.
package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shhac/agent-code-review/internal/store"
)

// handleQueue lists on GET and adds a PR on POST — mirroring `queue ls` and
// `queue add` so users can submit their own PRs from the dashboard.
func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listQueue(w, r)
	case http.MethodPost:
		s.addToQueue(w, r)
	default:
		httpError(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

func (s *Server) listQueue(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	candidates, err := s.store.ListCandidates(ctx, store.Filter{})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": candidates})
}

var (
	repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	// prRefPattern matches a PR reference in URL syntax, with or without the
	// https://github.com/ prefix: "owner/repo/pull/123" works bare.
	prRefPattern = regexp.MustCompile(`^(?:https://github\.com/)?([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/pull/([0-9]+)`)
)

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
	if !repoPattern.MatchString(req.Repo) || req.Number <= 0 {
		httpError(w, http.StatusBadRequest, `need {"url": "owner/repo/pull/N"} or {"repo": "owner/name", "number": N}`)
		return
	}
	if !s.repoWatched(req.Repo) {
		httpError(w, http.StatusForbidden, req.Repo+" is not a watched repo — see the Config page for the allowed list")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Requeue inserts new or flips an existing candidate back to queued,
	// preserving discovered metadata either way.
	err := s.store.Requeue(ctx, store.Candidate{
		Repo:         req.Repo,
		Number:       req.Number,
		Type:         store.TypeNew,
		URL:          "https://github.com/" + req.Repo + "/pull/" + strconv.Itoa(req.Number),
		Status:       store.StatusQueued,
		DiscoveredAt: time.Now(),
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queued": true})
}

// repoWatched reports whether repo is in the configured watch list
// (case-insensitive, matching GitHub's semantics).
func (s *Server) repoWatched(repo string) bool {
	for _, r := range s.config().Repos {
		if strings.EqualFold(r, repo) {
			return true
		}
	}
	return false
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
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
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
