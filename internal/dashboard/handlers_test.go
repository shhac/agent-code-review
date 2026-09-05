package dashboard

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/prref"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
)

// fakeStore fakes the handler-facing store surface; unused methods panic
// via the embedded nil interface so an unexpected dependency shows up loudly.
func newTestServer(fs *fakeStore, cfg config.Config) *Server {
	return testServer(withStore(fs), withConfig(cfg))
}

// serveJSON drives one handler call and decodes its JSON body: the shared
// httptest shape for every dashboard handler test. T picks the decode
// target: a typed response struct where one exists, map[string]any otherwise.
func serveJSON[T any](t *testing.T, h http.HandlerFunc, method, target, body string) (int, T) {
	t.Helper()
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(method, target, strings.NewReader(body)))
	var resp T
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("non-JSON response: %v (%s)", err, w.Body.String())
	}
	return w.Code, resp
}

func serveHandlerJSON[T any](t *testing.T, h http.Handler, method, target, body string) (int, T) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, target, strings.NewReader(body)))
	var resp T
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("non-JSON response: %v (%s)", err, w.Body.String())
	}
	return w.Code, resp
}

func doJSON(t *testing.T, h http.HandlerFunc, method, target, body string) (int, map[string]any) {
	t.Helper()
	return serveJSON[map[string]any](t, h, method, target, body)
}

func TestDashboardAPISmoke(t *testing.T) {
	now := time.Date(2026, 7, 8, 18, 30, 0, 0, time.UTC)
	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(review.LogPath(workDir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(review.LogPath(workDir), []byte("agent log tail\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	finished := store.Review{
		Repo:       "o/r",
		Number:     7,
		Title:      "Smoke review",
		Author:     "dev",
		HeadSHA:    "abc123",
		Verdict:    "COMMENTED",
		Engine:     "codex",
		ReviewedAt: now,
		WorkDir:    workDir,
	}
	finished.LogKey = store.ReviewLogKey(finished)
	fs := &fakeStore{
		queue: []store.Candidate{{
			Repo:         "o/r",
			Number:       8,
			Type:         store.TypeNew,
			Title:        "Queued review",
			Author:       "dev",
			HeadSHA:      "def456",
			DiscoveredAt: now,
		}},
		reviews:   []store.Review{finished},
		logReview: finished,
	}
	s := newTestServer(fs, config.Config{Repos: []string{"o/r"}, Schedule: config.ScheduleSettings{Enabled: config.Bool(true)}})
	s.version = "smoke"
	h := s.Handler()

	if code, resp := serveHandlerJSON[map[string]string](t, h, http.MethodGet, "/api/healthz", ""); code != http.StatusOK || resp["status"] != "ok" {
		t.Fatalf("healthz = %d %v", code, resp)
	}
	code, queue := serveHandlerJSON[struct {
		Candidates []queueView `json:"candidates"`
		Counts     queueCounts `json:"counts"`
	}](t, h, http.MethodGet, "/api/queue", "")
	if code != http.StatusOK || len(queue.Candidates) != 1 || queue.Counts.Total != 1 {
		t.Fatalf("queue smoke = %d %+v", code, queue)
	}
	code, reviews := serveHandlerJSON[struct {
		Reviews []store.Review `json:"reviews"`
	}](t, h, http.MethodGet, "/api/reviews?limit=5", "")
	if code != http.StatusOK || len(reviews.Reviews) != 1 || reviews.Reviews[0].LogKey == "" {
		t.Fatalf("reviews smoke = %d %+v", code, reviews)
	}
	code, cfg := serveHandlerJSON[map[string]any](t, h, http.MethodGet, "/api/config", "")
	if code != http.StatusOK || cfg["version"] != "smoke" {
		t.Fatalf("config smoke = %d %v", code, cfg)
	}
	logPath := "/api/review-log?repo=o/r&number=7&review=" + store.ReviewLogKey(finished)
	code, logResp := serveHandlerJSON[reviewLogResp](t, h, http.MethodGet, logPath, "")
	if code != http.StatusOK || !logResp.Available || logResp.Content != "agent log tail\n" || logResp.PR == nil || logResp.PR.Title != "Smoke review" {
		t.Fatalf("review-log smoke = %d %+v", code, logResp)
	}
}

// TestHandleQueue pins the queue surface end-to-end over a fake store: the
// GET envelope the Overview page consumes, and the add/remove gates.
func TestHandleQueue(t *testing.T) {
	watched := config.Config{Repos: []string{"o/r"}}

	t.Run("GET lists candidates with counts", func(t *testing.T) {
		fresh := time.Now().Add(-time.Minute)
		fs := &fakeStore{queue: []store.Candidate{
			{Repo: "o/r", Number: 1},
			{Repo: "o/r", Number: 2, ClaimedAt: &fresh},
		}}
		code, resp := doJSON(t, newTestServer(fs, watched).handleQueue, http.MethodGet, "/api/queue", "")
		if code != http.StatusOK {
			t.Fatalf("code = %d", code)
		}
		counts := resp["counts"].(map[string]any)
		if counts["total"].(float64) != 2 || counts["queued"].(float64) != 1 || counts["reviewing"].(float64) != 1 {
			t.Errorf("counts = %v", counts)
		}
	})

	t.Run("POST add gates on watched repos", func(t *testing.T) {
		fs := &fakeStore{}
		code, resp := doJSON(t, newTestServer(fs, watched).handleQueue, http.MethodPost, "/api/queue", `{"url":"other/repo/pull/5"}`)
		if code != http.StatusForbidden {
			t.Errorf("unwatched repo must 403, got %d %v", code, resp)
		}
		if len(fs.enqueued) != 0 {
			t.Error("nothing may be enqueued for an unwatched repo")
		}
	})

	t.Run("POST add rejects the retired repo/number shape", func(t *testing.T) {
		fs := &fakeStore{}
		if code, _ := doJSON(t, newTestServer(fs, watched).handleQueue, http.MethodPost, "/api/queue", `{"repo":"o/r","number":5}`); code != http.StatusBadRequest {
			t.Errorf("repo/number body must 400 (url-only wire shape), got %d", code)
		}
	})

	t.Run("POST add accepts a PR URL", func(t *testing.T) {
		fs := &fakeStore{}
		code, resp := doJSON(t, newTestServer(fs, watched).handleQueue, http.MethodPost, "/api/queue", `{"url":"https://github.com/o/r/pull/7"}`)
		if code != http.StatusOK || resp["queued"] != true {
			t.Fatalf("add must succeed, got %d %v", code, resp)
		}
		if len(fs.enqueued) != 1 || fs.enqueued[0].Number != 7 || fs.enqueued[0].Source != store.SourceManual {
			t.Errorf("enqueued = %+v, want manual o/r#7", fs.enqueued)
		}
	})

	t.Run("POST add rejects garbage", func(t *testing.T) {
		fs := &fakeStore{}
		if code, _ := doJSON(t, newTestServer(fs, watched).handleQueue, http.MethodPost, "/api/queue", `{"url":"not a pr"}`); code != http.StatusBadRequest {
			t.Errorf("garbage URL must 400, got %d", code)
		}
	})

	t.Run("DELETE removes", func(t *testing.T) {
		fs := &fakeStore{}
		code, _ := doJSON(t, newTestServer(fs, watched).handleQueue, http.MethodDelete, "/api/queue", `{"repo":"o/r","number":3}`)
		if code != http.StatusOK || len(fs.dequeued) != 1 || fs.dequeued[0].Number != 3 {
			t.Errorf("remove must dequeue o/r#3, got %d %v", code, fs.dequeued)
		}
	})
}

// TestHandleQueuePromote pins the "review this now" endpoint: it delegates
// to Store.Promote (top of queue + hold cleared + manual escalation) and
// validates its input like the other queue writes.
func TestHandleQueuePromote(t *testing.T) {
	t.Run("POST promotes", func(t *testing.T) {
		fs := &fakeStore{}
		code, resp := doJSON(t, newTestServer(fs, config.Config{}).handleQueuePromote, http.MethodPost, "/api/queue/promote", `{"repo":"o/r","number":9}`)
		if code != http.StatusOK || resp["promoted"] != true {
			t.Fatalf("code = %d resp = %v", code, resp)
		}
		if len(fs.promoted) != 1 || fs.promoted[0] != (prref.Ref{Repo: "o/r", Number: 9}) {
			t.Errorf("promote calls = %v", fs.promoted)
		}
	})
	t.Run("rejects garbage and non-POST", func(t *testing.T) {
		fs := &fakeStore{}
		if code, _ := doJSON(t, newTestServer(fs, config.Config{}).handleQueuePromote, http.MethodPost, "/api/queue/promote", `{"repo":"nonsense","number":0}`); code != http.StatusBadRequest {
			t.Errorf("garbage body must 400, got %d", code)
		}
		if code, _ := doJSON(t, newTestServer(fs, config.Config{}).handleQueuePromote, http.MethodGet, "/api/queue/promote", ""); code != http.StatusMethodNotAllowed {
			t.Errorf("GET must 405, got %d", code)
		}
		if len(fs.promoted) != 0 {
			t.Error("nothing may be promoted on invalid input")
		}
	})
}

// TestHandleQueueReorder pins the write path above the (already-tested)
// validator: a valid full ordering lands in one atomic store call, in order.
func TestHandleQueueReorder(t *testing.T) {
	fs := &fakeStore{queue: []store.Candidate{
		{Repo: "o/r", Number: 1},
		{Repo: "o/r", Number: 2},
	}}
	s := newTestServer(fs, config.Config{})
	code, resp := doJSON(t, s.handleQueueReorder, http.MethodPost, "/api/queue/reorder", `{"order":[{"repo":"o/r","number":2},{"repo":"o/r","number":1}]}`)
	if code != http.StatusOK || resp["reordered"] != true {
		t.Fatalf("reorder must succeed, got %d %v", code, resp)
	}
	if len(fs.positions) != 2 || fs.positions[0] != (store.QueuePosition{Repo: "o/r", Number: 2, Position: 1}) || fs.positions[1] != (store.QueuePosition{Repo: "o/r", Number: 1, Position: 2}) {
		t.Errorf("positions = %v, want 2 then 1", fs.positions)
	}

	if code, _ := doJSON(t, s.handleQueueReorder, http.MethodPost, "/api/queue/reorder", `{"order":[{"repo":"o/r","number":1}]}`); code != http.StatusBadRequest {
		t.Errorf("incomplete order must 400, got %d", code)
	}

	fail := &fakeStore{
		queue: []store.Candidate{
			{Repo: "o/r", Number: 1},
			{Repo: "o/r", Number: 2},
		},
		reorderErr: errors.New("write failed"),
	}
	if code, _ := doJSON(t, newTestServer(fail, config.Config{}).handleQueueReorder, http.MethodPost, "/api/queue/reorder", `{"order":[{"repo":"o/r","number":2},{"repo":"o/r","number":1}]}`); code != http.StatusInternalServerError {
		t.Errorf("Reorder failure must 500, got %d", code)
	}
	if len(fail.positions) != 0 {
		t.Errorf("failed Reorder should not record positions, got %v", fail.positions)
	}
}

// TestHandleUsage pins the no-cache branch: token sums come from the store
// even when the daemon isn't polling codex usage.
func TestHandleUsage(t *testing.T) {
	fs := &fakeStore{tokens: map[bool]int64{true: 500000, false: 12000}}
	code, resp := doJSON(t, newTestServer(fs, config.Config{}).handleUsage, http.MethodGet, "/api/usage", "")
	if code != http.StatusOK || resp["available"] != false {
		t.Fatalf("no usage cache must report available:false, got %d %v", code, resp)
	}
	if resp["fresh_tokens_total"].(float64) != 500000 || resp["fresh_tokens_24h"].(float64) != 12000 {
		t.Errorf("token sums = %v / %v", resp["fresh_tokens_total"], resp["fresh_tokens_24h"])
	}
}

// TestHandleConfig pins the fields the Config page renders, including the
// build version and the boot-pinned loop states.
func TestHandleConfig(t *testing.T) {
	fs := &fakeStore{}
	s := newTestServer(fs, config.Config{
		Repos:                   []string{"zeta/api", "Alpha/web", "alpha/admin"},
		AllowedAuthorsOnlyRepos: []string{"Alpha/web"},
		Review: config.ReviewSettings{Codex: config.CodexSettings{
			Model: "gpt-5.6-terra", Effort: "high",
		}},
	})
	s.version = "1.2.3"
	s.running = Running{Review: true}
	code, resp := doJSON(t, s.handleConfig, http.MethodGet, "/api/config", "")
	if code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}
	if resp["version"] != "1.2.3" {
		t.Errorf("version = %v", resp["version"])
	}
	if resp["review_running"] != true || resp["discovery_running"] != false {
		t.Errorf("running flags = %v / %v", resp["review_running"], resp["discovery_running"])
	}
	// The dials reported are the ACTIVE engine's, so the dashboard shows what
	// will actually run rather than always codex's.
	engineCfg := resp["engine_config"].(map[string]any)
	if resp["engine"] != "codex" || engineCfg["model"] != "gpt-5.6-terra" || engineCfg["effort"] != "high" {
		t.Errorf("engine=%v config = %v", resp["engine"], engineCfg)
	}
	repos := resp["repos"].([]any)
	got := make([]string, 0, len(repos))
	for _, raw := range repos {
		row := raw.(map[string]any)
		got = append(got, row["name"].(string))
		if row["name"] == "Alpha/web" && row["allowed_authors_only"] != true {
			t.Errorf("scoped repo lost allowed_authors_only flag: %v", row)
		}
	}
	want := []string{"alpha/admin", "Alpha/web", "zeta/api"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("repos = %v, want %v", got, want)
	}
}

// TestSPAHandler pins the asset-vs-route split: real files are served,
// missing assets 404 (broken script URLs stay visible), and everything else
// falls through to index.html so the frontend owns page routes.
func TestSPAHandler(t *testing.T) {
	files := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("SHELL")},
		"app.js":     &fstest.MapFile{Data: []byte("JS")},
	}
	h := spaHandler(files)
	get := func(path string) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w.Code, w.Body.String()
	}
	if code, body := get("/"); code != http.StatusOK || body != "SHELL" {
		t.Errorf("/ = %d %q, want the shell", code, body)
	}
	if code, body := get("/app.js"); code != http.StatusOK || body != "JS" {
		t.Errorf("/app.js = %d %q, want the asset", code, body)
	}
	if code, _ := get("/missing.js"); code != http.StatusNotFound {
		t.Errorf("missing asset = %d, want 404", code)
	}
	if code, body := get("/review/o/r/5"); code != http.StatusOK || body != "SHELL" {
		t.Errorf("SPA route = %d %q, want the shell", code, body)
	}
}

// TestSPACaching pins the upgrade-visibility contract: the unhashed shell
// must revalidate every load (a cached pre-upgrade bundle against a new
// daemon hides new UI: the "no promote button on held rows" bug), while
// content-hashed assets/ may cache forever.
func TestSPACaching(t *testing.T) {
	files := fstest.MapFS{
		"index.html":       &fstest.MapFile{Data: []byte("SHELL")},
		"mascot.webp":      &fstest.MapFile{Data: []byte("IMG")},
		"assets/index.js":  &fstest.MapFile{Data: []byte("JS")},
		"assets/index.css": &fstest.MapFile{Data: []byte("CSS")},
	}
	h := spaHandler(files)
	cacheOf := func(path string) string {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w.Header().Get("Cache-Control")
	}
	for _, p := range []string{"/", "/index.html", "/mascot.webp", "/config"} {
		if got := cacheOf(p); got != "no-cache" {
			t.Errorf("%s Cache-Control = %q, want no-cache", p, got)
		}
	}
	for _, p := range []string{"/assets/index.js", "/assets/index.css"} {
		if got := cacheOf(p); got != "public, max-age=31536000, immutable" {
			t.Errorf("%s Cache-Control = %q, want immutable", p, got)
		}
	}
}

// TestReviewsEndpointSearchesServerSide covers what the endpoint has to do
// that the browser cannot: match every row rather than the page it returns,
// and say how many matched. The page used to filter a fixed window locally,
// which reported a subset as the whole answer.
func TestReviewsEndpointSearchesServerSide(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Second)
	var rows []store.Review
	for i := range 21 {
		rows = append(rows, store.Review{
			Repo: "o/r", Number: 100 + i, Author: "nicole", Title: "change",
			Verdict: store.VerdictCommented, ReviewedAt: at.Add(-time.Duration(i) * time.Minute),
		})
	}
	for i := range 40 {
		rows = append(rows, store.Review{
			Repo: "o/r", Number: 500 + i, Author: "someone-else", Title: "other",
			Verdict: store.VerdictCommented, ReviewedAt: at.Add(time.Duration(i+1) * time.Minute),
		})
	}
	fs := &fakeStore{reviews: rows}
	h := newTestServer(fs, config.Config{Repos: []string{"o/r"}}).Handler()

	code, page := serveHandlerJSON[reviewsResp](t, h, http.MethodGet, "/api/reviews?q=nicole&limit=5", "")
	if code != http.StatusOK {
		t.Fatalf("search = %d", code)
	}
	if page.Total != 21 {
		t.Errorf("total must count every match, not the page: got %d want 21", page.Total)
	}
	if len(page.Reviews) != 5 {
		t.Errorf("page must hold limit rows: got %d", len(page.Reviews))
	}
	if page.NextCursor == "" {
		t.Fatal("a full page must carry a cursor for the next one")
	}
	if fs.lastQuery.Text != "nicole" {
		t.Errorf("the handler must forward q to the store, got %q", fs.lastQuery.Text)
	}

	// The cursor round-trips through the URL and lands on the same search.
	code, next := serveHandlerJSON[reviewsResp](t, h, http.MethodGet,
		"/api/reviews?q=nicole&limit=5&cursor="+url.QueryEscape(page.NextCursor), "")
	if code != http.StatusOK || next.Total != 21 || len(next.Reviews) == 0 {
		t.Fatalf("second page = %d %+v", code, next)
	}
	if next.Reviews[0].Number == page.Reviews[0].Number {
		t.Error("the second page must start after the first, not repeat it")
	}
}

// TestReviewsEndpointRefusesABadCursor pins the choice not to fall back to the
// first page. A cursor the server did not mint, or one from another search,
// means the page and the server disagree about where the reader is; showing
// page 1 under a pager that reads "3/18" hides that.
func TestReviewsEndpointRefusesABadCursor(t *testing.T) {
	h := newTestServer(&fakeStore{}, config.Config{Repos: []string{"o/r"}}).Handler()

	for _, tc := range []struct{ name, path string }{
		{"malformed", "/api/reviews?cursor=not-base64!!"},
		{"unknown sort", "/api/reviews?sort=cheapest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, resp := serveHandlerJSON[map[string]string](t, h, http.MethodGet, tc.path, "")
			if code != http.StatusBadRequest {
				t.Errorf("want 400, got %d (%v)", code, resp)
			}
			if resp["error"] == "" {
				t.Error("a refusal must say why")
			}
		})
	}
}
