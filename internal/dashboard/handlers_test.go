package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
)

// handlerStore fakes the handler-facing store surface; unused methods panic
// via the embedded nil interface so an unexpected dependency shows up loudly.
type handlerStore struct {
	store.Store

	queue     []store.Candidate
	reviews   []store.Review
	runs      []store.Run
	enqueued  []store.Candidate
	dequeued  []prRef
	positions []prRef        // SetQueuePos calls in order
	tokens    map[bool]int64 // keyed by since.IsZero()
}

func (f *handlerStore) ListQueue(context.Context, string) ([]store.Candidate, error) {
	return f.queue, nil
}

func (f *handlerStore) ListReviews(context.Context, int) ([]store.Review, error) {
	return f.reviews, nil
}

func (f *handlerStore) ListReviewsSince(context.Context, time.Time) ([]store.Review, error) {
	return f.reviews, nil
}

func (f *handlerStore) ListRuns(context.Context, int) ([]store.Run, error) {
	return f.runs, nil
}

func (f *handlerStore) Enqueue(_ context.Context, c store.Candidate) error {
	f.enqueued = append(f.enqueued, c)
	return nil
}

func (f *handlerStore) Dequeue(_ context.Context, repo string, number int) error {
	f.dequeued = append(f.dequeued, prRef{Repo: repo, Number: number})
	return nil
}

func (f *handlerStore) SetQueuePos(_ context.Context, repo string, number, _ int) error {
	f.positions = append(f.positions, prRef{Repo: repo, Number: number})
	return nil
}

func (f *handlerStore) TokensUsed(_ context.Context, since time.Time) (int64, error) {
	return f.tokens[since.IsZero()], nil
}

func newTestServer(fs *handlerStore, cfg config.Config) *Server {
	return &Server{
		store:  fs,
		config: func() config.Config { return cfg },
		manualCandidate: func(_ context.Context, repo string, number int) (store.Candidate, error) {
			return store.Candidate{Repo: repo, Number: number, Title: "T", Author: "a", HeadSHA: "sha", Source: store.SourceManual}, nil
		},
	}
}

func doJSON(t *testing.T, h http.HandlerFunc, method, target, body string) (int, map[string]any) {
	t.Helper()
	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("")
	} else {
		rdr = strings.NewReader(body)
	}
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(method, target, rdr))
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("non-JSON response: %v (%s)", err, w.Body.String())
	}
	return w.Code, resp
}

// TestHandleQueue pins the queue surface end-to-end over a fake store: the
// GET envelope the Overview page consumes, and the add/remove gates.
func TestHandleQueue(t *testing.T) {
	watched := config.Config{Repos: []string{"o/r"}}

	t.Run("GET lists candidates with counts", func(t *testing.T) {
		fresh := time.Now().Add(-time.Minute)
		fs := &handlerStore{queue: []store.Candidate{
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
		fs := &handlerStore{}
		code, resp := doJSON(t, newTestServer(fs, watched).handleQueue, http.MethodPost, "/api/queue", `{"repo":"other/repo","number":5}`)
		if code != http.StatusForbidden {
			t.Errorf("unwatched repo must 403, got %d %v", code, resp)
		}
		if len(fs.enqueued) != 0 {
			t.Error("nothing may be enqueued for an unwatched repo")
		}
	})

	t.Run("POST add accepts a PR URL", func(t *testing.T) {
		fs := &handlerStore{}
		code, resp := doJSON(t, newTestServer(fs, watched).handleQueue, http.MethodPost, "/api/queue", `{"url":"https://github.com/o/r/pull/7"}`)
		if code != http.StatusOK || resp["queued"] != true {
			t.Fatalf("add must succeed, got %d %v", code, resp)
		}
		if len(fs.enqueued) != 1 || fs.enqueued[0].Number != 7 || fs.enqueued[0].Source != store.SourceManual {
			t.Errorf("enqueued = %+v, want manual o/r#7", fs.enqueued)
		}
	})

	t.Run("POST add rejects garbage", func(t *testing.T) {
		fs := &handlerStore{}
		if code, _ := doJSON(t, newTestServer(fs, watched).handleQueue, http.MethodPost, "/api/queue", `{"url":"not a pr"}`); code != http.StatusBadRequest {
			t.Errorf("garbage URL must 400, got %d", code)
		}
	})

	t.Run("DELETE removes", func(t *testing.T) {
		fs := &handlerStore{}
		code, _ := doJSON(t, newTestServer(fs, watched).handleQueue, http.MethodDelete, "/api/queue", `{"repo":"o/r","number":3}`)
		if code != http.StatusOK || len(fs.dequeued) != 1 || fs.dequeued[0].Number != 3 {
			t.Errorf("remove must dequeue o/r#3, got %d %v", code, fs.dequeued)
		}
	})
}

// TestHandleQueueReorder pins the write path above the (already-tested)
// validator: a valid full ordering lands one SetQueuePos per row, in order.
func TestHandleQueueReorder(t *testing.T) {
	fs := &handlerStore{queue: []store.Candidate{
		{Repo: "o/r", Number: 1},
		{Repo: "o/r", Number: 2},
	}}
	s := newTestServer(fs, config.Config{})
	code, resp := doJSON(t, s.handleQueueReorder, http.MethodPost, "/api/queue/reorder", `{"order":[{"repo":"o/r","number":2},{"repo":"o/r","number":1}]}`)
	if code != http.StatusOK || resp["reordered"] != true {
		t.Fatalf("reorder must succeed, got %d %v", code, resp)
	}
	if len(fs.positions) != 2 || fs.positions[0].Number != 2 || fs.positions[1].Number != 1 {
		t.Errorf("positions = %v, want 2 then 1", fs.positions)
	}

	if code, _ := doJSON(t, s.handleQueueReorder, http.MethodPost, "/api/queue/reorder", `{"order":[{"repo":"o/r","number":1}]}`); code != http.StatusBadRequest {
		t.Errorf("incomplete order must 400, got %d", code)
	}
}

// TestHandleUsage pins the no-cache branch: token sums come from the store
// even when the daemon isn't polling codex usage.
func TestHandleUsage(t *testing.T) {
	fs := &handlerStore{tokens: map[bool]int64{true: 500000, false: 12000}}
	code, resp := doJSON(t, newTestServer(fs, config.Config{}).handleUsage, http.MethodGet, "/api/usage", "")
	if code != http.StatusOK || resp["available"] != false {
		t.Fatalf("no usage cache must report available:false, got %d %v", code, resp)
	}
	if resp["tokens_total"].(float64) != 500000 || resp["tokens_24h"].(float64) != 12000 {
		t.Errorf("token sums = %v / %v", resp["tokens_total"], resp["tokens_24h"])
	}
}

// TestHandleConfig pins the fields the Config page renders, including the
// build version and the boot-pinned loop states.
func TestHandleConfig(t *testing.T) {
	fs := &handlerStore{}
	s := newTestServer(fs, config.Config{Repos: []string{"o/r"}})
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
