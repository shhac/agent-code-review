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

func (s *Server) handleReviews(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	reviews, err := s.store.ListReviews(ctx, 50)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reviews": reviews})
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	runs, err := s.store.ListRuns(ctx, 20)
	if err != nil {
		s.fail(w, err)
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

// statsBucket is one hour of review outcomes in the /api/stats response.
type statsBucket struct {
	Hour             string `json:"hour"`
	Approved         int    `json:"approved"`
	Commented        int    `json:"commented"`
	RequestedChanges int    `json:"requested_changes"`
}

// bucketReviews aggregates reviews into 24 hourly buckets starting at start
// (which must be hour-aligned). Reviews outside [start, start+24h) are
// dropped; SKIPPED/ERROR verdicts don't count as outcomes. Pure — the
// hour-index math and verdict mapping are unit-tested directly.
func bucketReviews(reviews []store.Review, start time.Time) []statsBucket {
	buckets := make([]statsBucket, 24)
	for i := range buckets {
		buckets[i].Hour = start.Add(time.Duration(i) * time.Hour).Format(time.RFC3339)
	}
	for _, rv := range reviews {
		at := rv.ReviewedAt.UTC()
		// Duration division truncates toward zero, so a negative sub-hour
		// offset would land in bucket 0 — guard Before() explicitly.
		if at.Before(start) {
			continue
		}
		i := int(at.Sub(start) / time.Hour)
		if i >= 24 {
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
	return buckets
}

// handleStats returns 24 hourly buckets of review outcomes for the sliding
// last-24h window: approved / commented / requested_changes counts per hour.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	start := time.Now().UTC().Truncate(time.Hour).Add(-23 * time.Hour)
	reviews, err := s.store.ListReviewsSince(ctx, start)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"buckets": bucketReviews(reviews, start)})
}

func (s *Server) handleAuthors(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	authors, err := s.store.ListAllowedAuthors(ctx, r.URL.Query().Get("repo"))
	if err != nil {
		s.fail(w, err)
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

// fail writes the standard 500 error envelope.
func (s *Server) fail(w http.ResponseWriter, err error) {
	httpError(w, http.StatusInternalServerError, err.Error())
}

// httpError writes the JSON error envelope with an explicit status.
func httpError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
