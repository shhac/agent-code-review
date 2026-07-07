// Package dashboard serves the web UI: embedded HTML pages plus a small JSON
// API over the store and config. Config and prompt views are read-only; the
// queue supports add and reorder. The serve command wraps the returned handler
// with the HTTP listener and optional Tailscale tunnel.
package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
	"github.com/shhac/agent-code-review/internal/usage"
)

//go:embed assets/*
var assets embed.FS

// Server renders the queue, config, and prompt views. Config comes through a
// getter so edits to config.json show up without restarting the daemon.
// schedulerOn is whether THIS process is running the scheduler — the effective
// state after --no-schedule, which config alone can't tell.
type Server struct {
	store       store.Store
	config      func() config.Config
	schedulerOn bool
	usage       *usage.Cache // nil when the daemon isn't polling usage

	// ghUser resolves the login the gh CLI acts as; resolved once, lazily —
	// the Config page shows "reviewing as @…" so visitors know whose reviews
	// these will be.
	ghUser     func(ctx context.Context) (string, error)
	ghUserOnce sync.Once
	ghUserVal  string
}

func NewServer(s store.Store, cfg func() config.Config, schedulerOn bool, u *usage.Cache, ghUser func(ctx context.Context) (string, error)) *Server {
	return &Server{store: s, config: cfg, schedulerOn: schedulerOn, usage: u, ghUser: ghUser}
}

// reviewingAs returns the identity reviews are posted as: the configured
// gh_user override, else the lazily resolved gh login ("" if unresolvable).
func (s *Server) reviewingAs(ctx context.Context) string {
	if u := s.config().GHUser; u != "" {
		return u
	}
	if s.ghUser == nil {
		return ""
	}
	s.ghUserOnce.Do(func() {
		if u, err := s.ghUser(ctx); err == nil {
			s.ghUserVal = u
		}
	})
	return s.ghUserVal
}

// Handler returns the dashboard's HTTP routes. Config and prompt are
// read-only; the queue supports add and reorder (the same operations the
// `queue` CLI offers).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/queue", s.handleQueue)
	mux.HandleFunc("/api/queue/move", s.handleQueueMove)
	mux.HandleFunc("/api/reviews", s.handleReviews)
	mux.HandleFunc("/api/runs", s.handleRuns)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/usage", s.handleUsage)
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/authors", s.handleAuthors)
	mux.HandleFunc("/api/prompt", s.handlePrompt)
	mux.HandleFunc("/api/healthz", s.handleHealth)
	mux.Handle("/", http.FileServer(http.FS(mustSub())))
	return mux
}

// handleQueue lists on GET and adds a PR on POST — mirroring `queue ls` and
// `queue add` so users can submit their own PRs from the dashboard.
func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listQueue(w, r)
	case http.MethodPost:
		s.addToQueue(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST"})
	}
}

func (s *Server) listQueue(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	candidates, err := s.store.ListCandidates(ctx, store.Filter{})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if req.URL != "" {
		m := prRefPattern.FindStringSubmatch(strings.TrimSpace(req.URL))
		if m == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a PR reference — expected https://github.com/owner/repo/pull/N or owner/repo/pull/N"})
			return
		}
		req.Repo = m[1]
		req.Number, _ = strconv.Atoi(m[2])
	}
	if !repoPattern.MatchString(req.Repo) || req.Number <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `need {"url": "owner/repo/pull/N"} or {"repo": "owner/name", "number": N}`})
		return
	}
	if !s.repoWatched(req.Repo) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": req.Repo + " is not a watched repo — see the Config page for the allowed list"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// An existing candidate keeps its discovered metadata — just requeue it.
	if _, exists, err := s.store.GetCandidate(ctx, req.Repo, req.Number); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	} else if exists {
		if err := s.store.SetStatus(ctx, req.Repo, req.Number, store.StatusQueued); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"requeued": true})
		return
	}
	err := s.store.UpsertCandidate(ctx, store.Candidate{
		Repo:         req.Repo,
		Number:       req.Number,
		Type:         store.TypeNew,
		URL:          "https://github.com/" + req.Repo + "/pull/" + strconv.Itoa(req.Number),
		Status:       store.StatusQueued,
		DiscoveredAt: time.Now(),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": true})
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
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req struct {
		Repo      string `json:"repo"`
		Number    int    `json:"number"`
		Direction string `json:"direction"` // "up" | "down"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.Direction != "up" && req.Direction != "down") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `need {"repo", "number", "direction": "up"|"down"}`})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	queued, err := s.store.ListCandidates(ctx, store.Filter{Status: store.StatusQueued})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	i := -1
	for idx, c := range queued {
		if c.Repo == req.Repo && c.Number == req.Number {
			i = idx
			break
		}
	}
	if i < 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "PR is not in the queued list"})
		return
	}
	j := i - 1
	if req.Direction == "down" {
		j = i + 1
	}
	if j >= 0 && j < len(queued) {
		queued[i], queued[j] = queued[j], queued[i]
	}
	for pos, c := range queued {
		if err := s.store.SetQueuePos(ctx, c.Repo, c.Number, pos+1); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"moved": true})
}

func (s *Server) handleReviews(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	reviews, err := s.store.ListReviews(ctx, 50)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reviews": reviews})
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	runs, err := s.store.ListRuns(ctx, 20)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

// handleConfig returns the operational settings the UI shows: watched repos and
// the resolved dials (with defaults applied), not the raw file.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.config()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, map[string]any{
		"reviewing_as": s.reviewingAs(ctx),
		"repos":        cfg.Repos,
		"candidates": map[string]any{
			"new_max_age_days":       int(cfg.NewMaxAge().Hours() / 24),
			"refreshed_max_age_days": int(cfg.RefreshedMaxAge().Hours() / 24),
		},
		"schedule": map[string]any{
			"enabled":      cfg.Schedule.Enabled,
			"interval":     cfg.Interval().String(),
			"max_parallel": cfg.MaxParallel(),
		},
		// The effective state of THIS daemon: config may say enabled while the
		// process was started with --no-schedule (or vice-versa scenarios later).
		"scheduler_running": s.schedulerOn,
		"engine":            cfg.Engine(),
	})
}

// handleUsage returns the cached Codex rate-limit snapshot (refreshed by the
// daemon on dashboard.usage_poll_interval).
func (s *Server) handleUsage(w http.ResponseWriter, _ *http.Request) {
	if s.usage == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	snap := s.usage.Get()
	writeJSON(w, http.StatusOK, map[string]any{"available": !snap.FetchedAt.IsZero(), "usage": snap})
}

// handleStats returns 24 hourly buckets of review outcomes for the sliding
// last-24h window: approved / commented / requested_changes counts per hour.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	now := time.Now().UTC()
	start := now.Truncate(time.Hour).Add(-23 * time.Hour)
	reviews, err := s.store.ListReviewsSince(ctx, start)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	type bucket struct {
		Hour             string `json:"hour"`
		Approved         int    `json:"approved"`
		Commented        int    `json:"commented"`
		RequestedChanges int    `json:"requested_changes"`
	}
	buckets := make([]bucket, 24)
	for i := range buckets {
		buckets[i].Hour = start.Add(time.Duration(i) * time.Hour).Format(time.RFC3339)
	}
	for _, rv := range reviews {
		i := int(rv.ReviewedAt.UTC().Sub(start) / time.Hour)
		if i < 0 || i >= 24 {
			continue
		}
		switch rv.Verdict {
		case review.DecisionApproved:
			buckets[i].Approved++
		case review.DecisionCommented:
			buckets[i].Commented++
		case review.DecisionRequestedChanges:
			buckets[i].RequestedChanges++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"buckets": buckets})
}

func (s *Server) handleAuthors(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	authors, err := s.store.ListAllowedAuthors(ctx, r.URL.Query().Get("repo"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authors": authors})
}

// handlePrompt exposes the review prompt read-only: the main prompt, the rule
// fragments, and two fully assembled previews (allowed vs not-allowed author)
// built from a synthetic candidate so you can see exactly what the agent gets.
// The engine driver appends its own reporting instruction on top of this.
func (s *Server) handlePrompt(w http.ResponseWriter, _ *http.Request) {
	cfg := s.config()
	sample := store.Candidate{
		Repo:    "example-org/example-repo",
		Number:  123,
		Type:    store.TypeNew,
		Author:  "example-author",
		URL:     "https://github.com/example-org/example-repo/pull/123",
		HeadSHA: "0000000000000000000000000000000000000000",
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"main_prompt": review.MainPrompt(cfg.Review),
		"outcomes": map[string]string{
			"on_approve": cfg.Review.OnApprove,
			"on_comment": cfg.Review.OnComment,
			"on_reject":  cfg.Review.OnReject,
		},
		"rules": cfg.Review.Rules,
		"previews": map[string]string{
			"allowed_author":     review.BuildPrompt(cfg, sample, review.Facts{AuthorAllowed: true}),
			"not_allowed_author": review.BuildPrompt(cfg, sample, review.Facts{}),
		},
		"note": "Previews use a synthetic candidate. The engine driver appends a reporting instruction (final message = JSON verdict) on top of this.",
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// mustSub roots the file server at the embedded assets/ dir so "/" serves
// index.html. The embed is validated at build time, so a failure here is a
// programming error.
func mustSub() fs.FS {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err)
	}
	return sub
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
