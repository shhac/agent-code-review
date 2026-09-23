// Package dashboard serves the web UI: embedded HTML pages plus a small JSON
// API over the store and config. Config and prompt views are read-only; the
// queue supports add and reorder. The serve command wraps the returned handler
// with the HTTP listener and optional Tailscale tunnel.
package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shhac/crew-code-review/internal/config"
	"github.com/shhac/crew-code-review/internal/discover"
	"github.com/shhac/crew-code-review/internal/logbuf"
	"github.com/shhac/crew-code-review/internal/store"
	"github.com/shhac/crew-code-review/internal/usage"
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

// dashboardStore is the dashboard's view of persistence: exactly the methods
// the web server calls, so a fake in one handler's test does not have to
// satisfy the scheduler's claims or the author mutations this package never
// performs.
//
// One interface, grouped by surface in comments rather than split into five
// named sub-interfaces. Those existed for a while, by analogy with
// scheduler.SchedulerStore and discover.candidateStore, where the narrowing is
// real: those packages TAKE the narrow interface as a constructor parameter,
// so the compiler enforces it. Here every handler is a method on *Server
// reaching s.store, so not one of the five was ever used as a parameter, a
// field, or a constraint — the segmentation read as an enforced boundary that
// nothing checked. The property it promised (a queue test need not fake
// roster methods) is delivered instead by fakes_test.go embedding this
// interface, so an unimplemented call panics rather than compiles.
type dashboardStore interface {
	// Queue.
	ListQueue(context.Context, string) ([]store.Candidate, error)
	Enqueue(context.Context, store.Candidate) error
	Dequeue(context.Context, string, int) error
	Promote(context.Context, string, int) error
	Reorder(context.Context, []store.QueuePosition) error
	LastOutcome(context.Context, string, int) (store.Review, bool, error)

	// History.
	ReviewByLogKey(context.Context, string, int, string) (store.Review, bool, error)
	SearchReviews(context.Context, store.ReviewQuery) (store.ReviewPage, error)
	ListReviewsSince(context.Context, time.Time) ([]store.Review, error)
	FreshTokens(context.Context, time.Time) (int64, error)
	// Scoring: the leaderboard aggregate.
	Leaderboard(context.Context, store.LeaderboardQuery) ([]store.AuthorScore, error)

	// Author roster, and the one identity question the tailnet layer asks:
	// which GitHub handle is this login.
	ListAuthors(ctx context.Context, repo, group string) ([]store.Author, error)
	AuthorGroup(ctx context.Context, repo, handle string) (config.Membership, error)
	AuthorByTailscaleLogin(ctx context.Context, login string) (store.Author, bool, error)

	// Steering: the write surface, the single-row read its authorisation check
	// needs, and the editing hold plus the mark dating its session. Reading
	// steering back needs nothing — it rides on the candidate, so ListQueue
	// already carries it.
	QueuedPR(ctx context.Context, repo string, number int) (store.Candidate, bool, error)
	SetSteering(ctx context.Context, repo string, number int, st store.Steering) error
	ClearSteering(ctx context.Context, repo string, number int) error
	SetHolds(ctx context.Context, repo string, number int, holds map[string]time.Time) error
	ClearHolds(ctx context.Context, repo string, number int, names ...string) error
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

	// trustProxyIdentity is false when this daemon serves over Funnel, where
	// requests come from the public internet with no identity Tailscale
	// vouches for. Fixed at boot: whether the header can mean anything is a
	// property of how the daemon was started, not of a request.
	trustProxyIdentity bool
}

// Deps is everything a Server is built from, matching scheduler.Deps. A struct
// rather than a positional list because the list had reached eight and ended
// in a bare bool, where the compiler cannot tell one caller's mistake from
// another's intent, and because every new server-level dependency was churning
// the one production call site and every test constructor.
//
// Store and Config are required; the rest default to their production
// implementations, so a caller states what it cares about.
type Deps struct {
	Store   dashboardStore
	Config  func() config.Config
	Running Running
	Usage   *usage.Cache
	GHUser  func(ctx context.Context) (string, error)
	Logs    *logbuf.Ring
	Version string

	// TrustProxyIdentity is false when this daemon serves over Funnel, where
	// requests arrive from the public internet with no identity Tailscale
	// vouches for. Named rather than positional: as the eighth argument it was
	// a bare bool at a call site that could not say what it meant.
	TrustProxyIdentity bool

	// ManualCandidate fetches live PR metadata for a manual queue add.
	// Defaults to discover.ManualCandidate; injected in tests so the add path
	// is exercisable without gh.
	ManualCandidate func(ctx context.Context, repo string, number int) (store.Candidate, error)
}

// NewServer builds a Server from d, filling unset optional fields.
func NewServer(d Deps) *Server {
	if d.ManualCandidate == nil {
		d.ManualCandidate = discover.ManualCandidate
	}
	return &Server{
		store: d.Store, config: d.Config, running: d.Running, usage: d.Usage,
		ghUser: d.GHUser, logs: d.Logs, version: d.Version,
		manualCandidate:    d.ManualCandidate,
		trustProxyIdentity: d.TrustProxyIdentity,
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
	mux.HandleFunc("/api/queue/preflight", s.handleQueuePreflight)
	mux.HandleFunc("/api/steering", s.handleSteering)
	mux.HandleFunc("/api/steering/hold", s.handleSteeringHold)
	mux.HandleFunc("/api/viewer", readOnly(s.handleViewer))
	mux.HandleFunc("/api/reviews", s.handleReviews)
	mux.HandleFunc("/api/config", readOnly(s.handleConfig))
	mux.HandleFunc("/api/usage", readOnly(s.handleUsage))
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/metrics", s.handleMetrics)
	mux.HandleFunc("/api/leaderboard", s.handleLeaderboard)
	mux.HandleFunc("/api/score/preview", s.handleScorePreview)
	mux.HandleFunc("/api/score/simulate", s.handleScoreSimulate)
	mux.HandleFunc("/api/authors", s.handleAuthors)
	mux.HandleFunc("/api/prompt", readOnly(s.handlePrompt))
	mux.HandleFunc("/api/prompt/preview", readOnly(s.handlePromptPreview))
	mux.HandleFunc("/api/logs", readOnly(s.handleLogs))
	mux.HandleFunc("/api/review-log", readOnly(s.handleReviewLog))
	mux.HandleFunc("/api/healthz", readOnly(s.handleHealth))
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

// maxBodyBytes bounds every request body the dashboard decodes. The largest
// legitimate one is a scoring document for /api/score/simulate, a few KB, so a
// megabyte is generous; unbounded, a single request could make the daemon
// buffer whatever it was sent, and under --tailscale funnel the sender is the
// public internet.
const maxBodyBytes = 1 << 20

// decodeBody reads a JSON request body into T, refusing one over
// maxBodyBytes. The bound lives here rather than at each endpoint because it
// used to live at one of them: every other write endpoint read its body
// unbounded.
func decodeBody[T any](w http.ResponseWriter, r *http.Request) (T, error) {
	var v T
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&v)
	return v, err
}

// serveGet is the one single-fetch GET transport frame: request-scoped
// timeout, the standard error envelope, and the JSON write, so a new
// endpoint cannot silently omit any of the three. Handlers keep only
// parameter parsing and response shaping inside fetch; multi-fetch or
// branching handlers (usage, config, review-log) stay explicit.
func serveGet[T any](s *Server, w http.ResponseWriter, r *http.Request, fetch func(context.Context) (T, error)) {
	// This frame IS the read surface, so the method check belongs here rather
	// than in each handler: without it every endpoint routed through it
	// answered POST and DELETE as cheerfully as GET, while the handlers that
	// check for themselves (the queue writes) did not.
	if !isRead(r) {
		refuseMethod(w)
		return
	}
	respond(s, w, r, 10*time.Second, fetch)
}

// readOnly gives a read handler that does not go through serveGet (it streams,
// or builds its response by hand) the same refusal of anything but GET and
// HEAD. Applied at the route rather than in each handler, so a new read
// endpoint gets it by being registered the way its neighbours are.
func readOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isRead(r) {
			refuseMethod(w)
			return
		}
		h(w, r)
	}
}

func isRead(r *http.Request) bool {
	return r.Method == http.MethodGet || r.Method == http.MethodHead
}

func refuseMethod(w http.ResponseWriter) {
	httpError(w, http.StatusMethodNotAllowed, "method not allowed")
}

// serveWrite is serveGet's write-side twin: decode the body, then act under a
// request-scoped timeout, with one exit through fail or writeJSON. A refusal
// is an apiErr RETURNED rather than a status written mid-handler, which is
// what the write handlers had drifted on: some wrote httpError with a status
// fail would have unwrapped anyway, and only one bounded its body.
//
// decode should read through decodeBody and return an apiErr (usually a 400)
// for a body it cannot use; it runs before the timeout starts, as the handlers
// always did.
//
// The method check stays with each handler, because the write endpoints do not
// share one: /api/queue sends two methods to two writers, and the steering
// hold takes POST or DELETE on one.
func serveWrite[Req, Resp any](s *Server, w http.ResponseWriter, r *http.Request, timeout time.Duration,
	decode func(http.ResponseWriter, *http.Request) (Req, error),
	act func(context.Context, Req) (Resp, error),
) {
	req, err := decode(w, r)
	if err != nil {
		s.fail(w, err)
		return
	}
	respond(s, w, r, timeout, func(ctx context.Context) (Resp, error) { return act(ctx, req) })
}

// respond is the tail both frames share: the deadline, the error envelope, and
// the JSON write.
func respond[T any](s *Server, w http.ResponseWriter, r *http.Request, timeout time.Duration, fn func(context.Context) (T, error)) {
	ctx, cancel := reqCtx(r, timeout)
	defer cancel()
	resp, err := fn(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// fail writes the error envelope: an apiErr carries its own status, anything
// else is a 500. The default stays 500 deliberately, so an error nobody has
// classified is reported as ours rather than blamed on the caller.
func (s *Server) fail(w http.ResponseWriter, err error) {
	var api *apiErr
	if errors.As(err, &api) {
		httpError(w, api.code, api.msg)
		return
	}
	httpError(w, http.StatusInternalServerError, err.Error())
}

// apiErr is a refusal with the status it should carry, so a check (the
// steering authorisation ladder above all) can be one function that returns
// "no, and here is the code" rather than a sequence of writes interleaved with
// transport concerns. Every handler shares it through fail.
type apiErr struct {
	code int
	msg  string
}

// Error makes a refusal usable as a plain error, which is what lets a handler
// inside the serveGet frame return one. Without it the frame's only vocabulary
// was 500, so a caller's own mistake (an unparseable cursor) was reported as
// the server having broken.
func (e *apiErr) Error() string { return e.msg }

// httpError writes the JSON error envelope with an explicit status.
func httpError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
