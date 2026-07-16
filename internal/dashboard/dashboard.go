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
	"github.com/shhac/agent-code-review/internal/discover"
	"github.com/shhac/agent-code-review/internal/logbuf"
	"github.com/shhac/agent-code-review/internal/store"
	"github.com/shhac/agent-code-review/internal/usage"
)

//go:embed assets/*
var assets embed.FS

// Running is the effective loop state of THIS daemon, what config alone
// can't tell after --no-schedule: whether the discovery and review loops are
// actually running in this process.
type Running struct {
	Discovery bool
	Review    bool
}

// dashboardStore is the dashboard's read/write view of persistence. The web
// server deliberately does not know about scheduler claims, run locks, or
// author mutations it never performs.
type dashboardStore interface {
	ListQueue(context.Context, string) ([]store.Candidate, error)
	Enqueue(context.Context, store.Candidate) error
	Dequeue(context.Context, string, int) error
	Promote(context.Context, string, int) error
	Reorder(context.Context, []store.QueuePosition) error
	LastOutcome(context.Context, string, int) (store.Review, bool, error)
	ReviewByLogKey(context.Context, string, int, string) (store.Review, bool, error)
	ListReviews(context.Context, int) ([]store.Review, error)
	ListReviewsSince(context.Context, time.Time) ([]store.Review, error)
	ListRuns(context.Context, int) ([]store.Run, error)
	TokensUsed(context.Context, time.Time) (int64, error)
	ListAllowedAuthors(context.Context, string) ([]store.AllowedAuthor, error)
}

// Server renders the queue, config, and prompt views. Config comes through a
// getter so edits to config.json show up without restarting the daemon.
type Server struct {
	store   dashboardStore
	config  func() config.Config
	running Running
	usage   *usage.Cache // nil when the daemon isn't polling usage
	logs    *logbuf.Ring // nil when the process doesn't capture logs
	version string       // ldflags-injected build version; "dev" outside releases

	// ghUser resolves the login the gh CLI acts as; resolved once, lazily.
	// The Config page shows "reviewing as @…" so visitors know whose reviews
	// these will be.
	ghUser     func(ctx context.Context) (string, error)
	ghUserOnce sync.Once
	ghUserVal  string

	// manualCandidate fetches live PR metadata for a manual queue add
	// (discover.ManualCandidate in production; injected in tests so the add
	// path is testable without gh).
	manualCandidate func(ctx context.Context, repo string, number int) (store.Candidate, error)
}

func NewServer(s dashboardStore, cfg func() config.Config, running Running, u *usage.Cache, ghUser func(ctx context.Context) (string, error), logs *logbuf.Ring, version string) *Server {
	return &Server{
		store: s, config: cfg, running: running, usage: u, ghUser: ghUser,
		logs: logs, version: version,
		manualCandidate: discover.ManualCandidate,
	}
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
// read-only; the queue supports add, reorder, remove, and promote (the same
// operations the `queue` CLI offers).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/queue", s.handleQueue)
	mux.HandleFunc("/api/queue/reorder", s.handleQueueReorder)
	mux.HandleFunc("/api/queue/promote", s.handleQueuePromote)
	mux.HandleFunc("/api/reviews", s.handleReviews)
	mux.HandleFunc("/api/runs", s.handleRuns)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/usage", s.handleUsage)
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/metrics", s.handleMetrics)
	mux.HandleFunc("/api/authors", s.handleAuthors)
	mux.HandleFunc("/api/prompt", s.handlePrompt)
	mux.HandleFunc("/api/prompt/preview", s.handlePromptPreview)
	mux.HandleFunc("/api/logs", s.handleLogs)
	mux.HandleFunc("/api/review-log", s.handleReviewLog)
	mux.HandleFunc("/api/healthz", s.handleHealth)
	mux.Handle("/", spaHandler(mustSub()))
	return mux
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
//
// Caching: Vite's assets/ filenames are content-hashed, so they may cache
// forever; everything else (the index.html shell above all) must revalidate
// every load, or a browser keeps running a pre-upgrade bundle against a new
// daemon; embed.FS files carry no modtime, so without an explicit header
// browsers heuristically cache the shell indefinitely.
func spaHandler(files fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "." || name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(files, name); err == nil {
			setSPACaching(w, name)
			http.ServeFileFS(w, r, files, name)
			return
		}
		if path.Ext(name) != "" && !strings.HasSuffix(name, ".html") {
			http.NotFound(w, r)
			return
		}
		setSPACaching(w, "index.html")
		http.ServeFileFS(w, r, files, "index.html")
	})
}

func setSPACaching(w http.ResponseWriter, name string) {
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
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
