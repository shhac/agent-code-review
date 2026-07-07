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
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/logbuf"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
	"github.com/shhac/agent-code-review/internal/usage"
)

//go:embed assets/*
var assets embed.FS

// Running is the effective loop state of THIS daemon — what config alone
// can't tell after --no-schedule: whether the discovery and review loops are
// actually running in this process.
type Running struct {
	Discovery bool
	Review    bool
}

// Server renders the queue, config, and prompt views. Config comes through a
// getter so edits to config.json show up without restarting the daemon.
type Server struct {
	store   store.Store
	config  func() config.Config
	running Running
	usage   *usage.Cache // nil when the daemon isn't polling usage
	logs    *logbuf.Ring // nil when the process doesn't capture logs

	// ghUser resolves the login the gh CLI acts as; resolved once, lazily —
	// the Config page shows "reviewing as @…" so visitors know whose reviews
	// these will be.
	ghUser     func(ctx context.Context) (string, error)
	ghUserOnce sync.Once
	ghUserVal  string
}

func NewServer(s store.Store, cfg func() config.Config, running Running, u *usage.Cache, ghUser func(ctx context.Context) (string, error), logs *logbuf.Ring) *Server {
	return &Server{store: s, config: cfg, running: running, usage: u, ghUser: ghUser, logs: logs}
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
	mux.HandleFunc("/api/queue/reorder", s.handleQueueReorder)
	mux.HandleFunc("/api/reviews", s.handleReviews)
	mux.HandleFunc("/api/runs", s.handleRuns)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/usage", s.handleUsage)
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/authors", s.handleAuthors)
	mux.HandleFunc("/api/prompt", s.handlePrompt)
	mux.HandleFunc("/api/logs", s.handleLogs)
	mux.HandleFunc("/api/healthz", s.handleHealth)
	mux.Handle("/", spaHandler(mustSub()))
	return mux
}

func (s *Server) handleReviews(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()
	reviews, err := s.store.ListReviews(ctx, queryInt(r, "limit", 50, 500))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reviews": reviews})
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r, 10*time.Second)
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
	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()
	repos := make([]map[string]any, 0, len(cfg.Repos))
	for _, r := range cfg.Repos {
		repos = append(repos, map[string]any{"name": r, "allowed_authors_only": cfg.AuthorScopedRepo(r)})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"reviewing_as": s.reviewingAs(ctx),
		"repos":        repos,
		"candidates": map[string]any{
			"new_max_age_days":       int(cfg.NewMaxAge().Hours() / 24),
			"refreshed_max_age_days": int(cfg.RefreshedMaxAge().Hours() / 24),
		},
		"schedule": map[string]any{
			"enabled":                    cfg.Schedule.Enabled,
			"interval":                   cfg.Interval().String(),
			"max_parallel":               cfg.MaxParallel(),
			"usage_floor_5h_percent":     cfg.UsageFloor5h(),
			"usage_floor_weekly_percent": cfg.UsageFloorWeekly(),
		},
		"discovery": map[string]any{
			"enabled":  cfg.Discovery.Enabled,
			"interval": cfg.DiscoverInterval().String(),
		},
		// The effective state of THIS daemon: config may say enabled while the
		// process was started with --no-schedule.
		"review_running":    s.running.Review,
		"discovery_running": s.running.Discovery,
		"engine":            cfg.Engine(),
	})
}

// handleUsage returns the cached Codex rate-limit snapshot (refreshed by the
// daemon on dashboard.usage_poll_interval) plus the usage-floor verdict the
// scheduler applies to it, so the UI can show why reviews are paused.
func (s *Server) handleUsage(w http.ResponseWriter, _ *http.Request) {
	if s.usage == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	snap := s.usage.Get()
	cfg := s.config()
	paused, reason := usage.BelowFloor(snap, cfg.UsageFloor5h(), cfg.UsageFloorWeekly())
	writeJSON(w, http.StatusOK, map[string]any{
		"available":     !snap.FetchedAt.IsZero(),
		"usage":         snap,
		"review_paused": paused,
		"paused_reason": reason,
	})
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
	ctx, cancel := reqCtx(r, 10*time.Second)
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
	ctx, cancel := reqCtx(r, 10*time.Second)
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

// handleLogs returns the newest captured daemon log lines, oldest first.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.logs == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "entries": []logbuf.Entry{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "entries": s.logs.Tail(queryInt(r, "n", 500, 1000))})
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

// spaHandler serves built dashboard assets and lets the frontend own page
// routes such as /config, /prompt, and /logs. Real missing asset files still
// return 404 so broken script/style URLs are visible during development.
func spaHandler(files fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "." || name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(files, name); err == nil {
			http.ServeFileFS(w, r, files, name)
			return
		}
		if path.Ext(name) != "" && !strings.HasSuffix(name, ".html") {
			http.NotFound(w, r)
			return
		}
		http.ServeFileFS(w, r, files, "index.html")
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// queryInt reads a bounded positive integer query parameter, falling back to
// def when the parameter is absent, malformed, or outside (0, max].
func queryInt(r *http.Request, key string, def, max int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil || v <= 0 || v > max {
		return def
	}
	return v
}

// reqCtx bounds a handler's work with the standard per-request deadline.
func reqCtx(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

// fail writes the standard 500 error envelope.
func (s *Server) fail(w http.ResponseWriter, err error) {
	httpError(w, http.StatusInternalServerError, err.Error())
}

// httpError writes the JSON error envelope with an explicit status.
func httpError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
