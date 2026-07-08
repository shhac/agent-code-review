package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
)

// TestReviewLogView pins the state resolution the ReviewLog page keys on:
// a live lease renders "reviewing", a stale or absent claim on a queued row
// renders "queued" (the next cycle will reclaim it), and a history row
// renders "finished" with its postmortem fields. The reviewing threshold
// must flip exactly at the lease boundary.
func TestReviewLogView(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	lease := 2 * time.Hour
	fresh := now.Add(-time.Hour)
	boundary := now.Add(-lease)
	stale := now.Add(-lease - time.Second)

	t.Run("fresh claim is reviewing", func(t *testing.T) {
		ws := store.Workspace{Dir: "/wd", Queued: &store.Candidate{Title: "T", Author: "a", URL: "u", ClaimedAt: &fresh}}
		state, pr := reviewLogView("o/r", 5, ws, now, lease)
		if state != "reviewing" {
			t.Errorf("state = %q, want reviewing", state)
		}
		if pr.Title != "T" || pr.Author != "a" || pr.URL != "u" || pr.ClaimedAt == nil {
			t.Errorf("pr must carry the queue row's header fields, got %+v", pr)
		}
	})

	t.Run("boundary claim is still reviewing", func(t *testing.T) {
		ws := store.Workspace{Dir: "/wd", Queued: &store.Candidate{ClaimedAt: &boundary}}
		if state, _ := reviewLogView("o/r", 5, ws, now, lease); state != "reviewing" {
			t.Errorf("state = %q; a claim aged exactly one window is still leased", state)
		}
	})

	t.Run("stale claim is queued", func(t *testing.T) {
		ws := store.Workspace{Dir: "/wd", Queued: &store.Candidate{ClaimedAt: &stale}}
		if state, _ := reviewLogView("o/r", 5, ws, now, lease); state != "queued" {
			t.Errorf("state = %q, want queued", state)
		}
	})

	t.Run("unclaimed queue row is queued", func(t *testing.T) {
		ws := store.Workspace{Dir: "/wd", Queued: &store.Candidate{}}
		if state, _ := reviewLogView("o/r", 5, ws, now, lease); state != "queued" {
			t.Errorf("state = %q, want queued", state)
		}
	})

	t.Run("history row is finished", func(t *testing.T) {
		reviewed := now.Add(-time.Hour)
		ws := store.Workspace{Dir: "/wd", Finished: &store.Review{Title: "T", Verdict: "APPROVED", DurationSecs: 90, ReviewedAt: reviewed}}
		state, pr := reviewLogView("o/r", 5, ws, now, lease)
		if state != "finished" {
			t.Errorf("state = %q, want finished", state)
		}
		if pr.Verdict != "APPROVED" || pr.DurationSecs != 90 || pr.ReviewedAt == nil || !pr.ReviewedAt.Equal(reviewed) {
			t.Errorf("pr must carry the postmortem fields, got %+v", pr)
		}
	})
}

// TestTailFile pins the tail-window math at the size==limit boundary: the
// returned size is always the FULL file size (the handler's truncated flag
// compares it against the limit), while content is capped at limit bytes
// from the end.
func TestTailFile(t *testing.T) {
	write := func(t *testing.T, content string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "agent.log")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	const limit = 8
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"empty file", "", ""},
		{"smaller than limit", "abc", "abc"},
		{"exactly at limit", "12345678", "12345678"},
		{"larger than limit keeps the tail", "0123456789", "23456789"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, size, err := tailFile(write(t, tc.content), limit)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("content = %q, want %q", got, tc.want)
			}
			if size != int64(len(tc.content)) {
				t.Errorf("size = %d, want the full file size %d", size, len(tc.content))
			}
			if truncated := size > limit; truncated != (len(tc.content) > limit) {
				t.Errorf("truncated = %v for %d bytes at limit %d", truncated, len(tc.content), limit)
			}
		})
	}

	t.Run("missing file errors", func(t *testing.T) {
		if _, _, err := tailFile(filepath.Join(t.TempDir(), "absent"), limit); err == nil {
			t.Error("missing log must surface an error, not an empty tail")
		}
	})
}

// reviewLogStore fakes the two reads handleReviewLog performs through
// store.FindWorkspace; everything else panics via the embedded nil interface.
type reviewLogStore struct {
	store.Store
	queue []store.Candidate
}

func (f *reviewLogStore) ListQueue(context.Context, string) ([]store.Candidate, error) {
	return f.queue, nil
}

func (f *reviewLogStore) LastOutcome(context.Context, string, int) (store.Review, bool, error) {
	return store.Review{}, false, nil
}

// TestHandleReviewLog covers the HTTP wiring above the tested pure cores:
// param validation, the nothing-recorded envelope, the resolved-but-missing-
// log error envelope, and the success envelope the ReviewLog page consumes.
func TestHandleReviewLog(t *testing.T) {
	get := func(t *testing.T, s *Server, target string) (int, reviewLogResp) {
		t.Helper()
		w := httptest.NewRecorder()
		s.handleReviewLog(w, httptest.NewRequest(http.MethodGet, target, nil))
		var resp reviewLogResp
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("response is not a review-log envelope: %v (%s)", err, w.Body.String())
		}
		return w.Code, resp
	}
	newServer := func(queue []store.Candidate) *Server {
		return &Server{
			store:  &reviewLogStore{queue: queue},
			config: func() config.Config { return config.Config{} },
		}
	}

	t.Run("invalid repo is a 400", func(t *testing.T) {
		if code, _ := get(t, newServer(nil), "/api/review-log?repo=not-a-repo&number=5"); code != http.StatusBadRequest {
			t.Errorf("code = %d, want 400", code)
		}
	})

	t.Run("missing number is a 400", func(t *testing.T) {
		if code, _ := get(t, newServer(nil), "/api/review-log?repo=o/r"); code != http.StatusBadRequest {
			t.Errorf("code = %d, want 400", code)
		}
	})

	t.Run("nothing recorded", func(t *testing.T) {
		code, resp := get(t, newServer(nil), "/api/review-log?repo=o/r&number=5")
		if code != http.StatusOK || resp.Available || resp.State != "" {
			t.Errorf("want empty available:false envelope, got %d %+v", code, resp)
		}
	})

	t.Run("live claim serves the log", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(review.LogPath(dir), []byte("agent output"), 0o600); err != nil {
			t.Fatal(err)
		}
		claimed := time.Now().Add(-time.Minute)
		code, resp := get(t, newServer([]store.Candidate{
			{Repo: "o/r", Number: 5, WorkDir: dir, ClaimedAt: &claimed},
		}), "/api/review-log?repo=o/r&number=5")
		if code != http.StatusOK || !resp.Available || resp.State != "reviewing" {
			t.Fatalf("want available reviewing, got %d %+v", code, resp)
		}
		if resp.Content != "agent output" || resp.Truncated {
			t.Errorf("content = %q truncated = %v, want the full log untruncated", resp.Content, resp.Truncated)
		}
		if resp.PR == nil || resp.PR.Repo != "o/r" || resp.PR.Number != 5 {
			t.Errorf("pr header = %+v, want o/r#5", resp.PR)
		}
	})

	t.Run("workspace without a log reports the error", func(t *testing.T) {
		code, resp := get(t, newServer([]store.Candidate{
			{Repo: "o/r", Number: 5, WorkDir: filepath.Join(t.TempDir(), "gone")},
		}), "/api/review-log?repo=o/r&number=5")
		if code != http.StatusOK || resp.Available {
			t.Fatalf("want 200 available:false, got %d %+v", code, resp)
		}
		if resp.Error == "" || resp.State != "queued" || resp.PR == nil {
			t.Errorf("error envelope must keep state and pr, got %+v", resp)
		}
	})
}
