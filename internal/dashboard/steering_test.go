package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
)

// fakeStore is the roster + queue + steering surface the identity and
// steering handlers touch. Embeds the full Store so an unexpected call panics.
func steerServer(fs *fakeStore, trust bool) *Server {
	opts := []serverOpt{withStore(fs), withConfig(config.Config{GHUser: "paul-gh"})}
	if trust {
		opts = append(opts, withTrustedProxy())
	}
	return testServer(opts...)
}

// post drives the steering handler with a chosen peer address and headers,
// which is the whole point: the two spoofing defences are about WHERE the
// request came from, not what it says.
func post(t *testing.T, s *Server, remote, login, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/steering", strings.NewReader(body))
	r.RemoteAddr = remote
	if login != "" {
		r.Header.Set(tailscaleLoginHeader, login)
	}
	w := httptest.NewRecorder()
	s.handleSteering(w, r)
	return w
}

const octoPR = `{"repo":"o/r","number":1,"message":"focus on the rollback path"}`

func queuedPR() *fakeStore {
	return &fakeStore{
		queue: []store.Candidate{{Repo: "o/r", Number: 1, Author: "octocat", HeadSHA: "s1"}},
		byLogin: map[string]store.Author{
			"octo@example.com":    {GitHubHandle: "octocat"},
			"paul@example.com":    {GitHubHandle: "paul-gh"},
			"mallory@example.com": {GitHubHandle: "mallory"},
		},
	}
}

// TestSteeringRejectsForgedIdentity is the security property: the header is
// proof only because Tailscale attached it, and the only requests Tailscale
// attached anything to are the ones it proxied. Everything else must be
// anonymous, however convincing the header looks.
func TestSteeringRejectsForgedIdentity(t *testing.T) {
	t.Run("a non-loopback peer is never identified", func(t *testing.T) {
		// What a direct hit on the port looks like if the listener is ever
		// bound wider than loopback: a real tailnet address, a header the
		// client wrote itself, and nothing in between to strip it.
		fs := queuedPR()
		w := post(t, steerServer(fs, true), "100.101.66.81:54321", "paul@example.com", octoPR)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401: a header on a direct connection is client-supplied", w.Code)
		}
		if len(fs.steered) != 0 {
			t.Errorf("nothing may be written, got %+v", fs.steered)
		}
	})

	t.Run("a public address is never identified", func(t *testing.T) {
		fs := queuedPR()
		w := post(t, steerServer(fs, true), "203.0.113.7:443", "paul@example.com", octoPR)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", w.Code)
		}
	})

	t.Run("funnel mode trusts no header at all", func(t *testing.T) {
		// Funnel carries public traffic Tailscale attaches no identity to, so
		// even a loopback peer (the funnel proxy itself) proves nothing.
		fs := queuedPR()
		w := post(t, steerServer(fs, false), "127.0.0.1:54321", "paul@example.com", octoPR)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 when serving over funnel", w.Code)
		}
		if len(fs.steered) != 0 {
			t.Errorf("nothing may be written, got %+v", fs.steered)
		}
	})

	t.Run("no header is anonymous, not permissive", func(t *testing.T) {
		fs := queuedPR()
		if w := post(t, steerServer(fs, true), "127.0.0.1:1", "", octoPR); w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", w.Code)
		}
	})
}

// TestSteeringAuthorisation pins who may steer what, once identity is proven.
func TestSteeringAuthorisation(t *testing.T) {
	t.Run("the PR author may steer their own", func(t *testing.T) {
		fs := queuedPR()
		w := post(t, steerServer(fs, true), "127.0.0.1:1", "octo@example.com", octoPR)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", w.Code, w.Body)
		}
		if len(fs.steered) != 1 || fs.steered[0].SetBy != "octocat" {
			t.Errorf("steering = %+v, want one row attributed to octocat", fs.steered)
		}
	})

	t.Run("a rostered stranger may not steer someone else's", func(t *testing.T) {
		fs := queuedPR()
		w := post(t, steerServer(fs, true), "127.0.0.1:1", "mallory@example.com", octoPR)
		if w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", w.Code)
		}
		if len(fs.steered) != 0 {
			t.Errorf("nothing may be written, got %+v", fs.steered)
		}
	})

	t.Run("the account reviews post as may steer any PR", func(t *testing.T) {
		fs := queuedPR()
		w := post(t, steerServer(fs, true), "127.0.0.1:1", "paul@example.com", octoPR)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", w.Code, w.Body)
		}
		if len(fs.steered) != 1 || fs.steered[0].SetBy != "paul-gh" {
			t.Errorf("steering = %+v", fs.steered)
		}
	})

	t.Run("an identified but unrostered person steers nothing", func(t *testing.T) {
		fs := queuedPR()
		w := post(t, steerServer(fs, true), "127.0.0.1:1", "stranger@example.com", octoPR)
		if w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403: authenticated but no roster row", w.Code)
		}
	})

	t.Run("authorisation reads the author from the store, not the request", func(t *testing.T) {
		// A caller cannot widen their rights by describing the PR differently:
		// the author comes from the queued row.
		fs := queuedPR()
		body := `{"repo":"o/r","number":1,"message":"x","author":"mallory"}`
		if w := post(t, steerServer(fs, true), "127.0.0.1:1", "mallory@example.com", body); w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", w.Code)
		}
	})

	t.Run("a mis-cased repo is not a different PR", func(t *testing.T) {
		// The store matches repo exactly, so this is a 404 rather than a hit.
		// Pinned because the handler used to carry a case-insensitive
		// comparison that the SQL filter made unreachable, reading as a
		// promise the store never kept.
		fs := queuedPR()
		body := `{"repo":"O/R","number":1,"message":"x"}`
		if w := post(t, steerServer(fs, true), "127.0.0.1:1", "paul@example.com", body); w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", w.Code)
		}
	})

	t.Run("an unqueued PR is a 404", func(t *testing.T) {
		fs := queuedPR()
		body := `{"repo":"o/r","number":99,"message":"x"}`
		if w := post(t, steerServer(fs, true), "127.0.0.1:1", "paul@example.com", body); w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", w.Code)
		}
	})

	t.Run("an empty message clears", func(t *testing.T) {
		fs := queuedPR()
		body := `{"repo":"o/r","number":1,"message":"   "}`
		if w := post(t, steerServer(fs, true), "127.0.0.1:1", "octo@example.com", body); w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		if len(fs.cleared) != 1 || len(fs.steered) != 0 {
			t.Errorf("cleared=%+v steered=%+v, want a clear and no write", fs.cleared, fs.steered)
		}
	})

	t.Run("an over-long message is refused", func(t *testing.T) {
		fs := queuedPR()
		long, _ := json.Marshal(strings.Repeat("x", store.SteeringMaxLen+1))
		body := `{"repo":"o/r","number":1,"message":` + string(long) + `}`
		if w := post(t, steerServer(fs, true), "127.0.0.1:1", "octo@example.com", body); w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})
}

// TestAddWithSteering pins the add-time path. It exists because there is no
// window afterwards: a manual add lands on an empty queue and a free
// dispatcher slot takes it within the idle poll, so an author steers at add
// time or not at all.
func TestAddWithSteering(t *testing.T) {
	server := func(fs *fakeStore) *Server {
		return testServer(
			withStore(fs),
			withTrustedProxy(),
			withConfig(config.Config{GHUser: "paul-gh", Repos: []string{"o/r"}}),
			withManualCandidate(func(_ context.Context, repo string, number int) (store.Candidate, error) {
				return store.Candidate{Repo: repo, Number: number, Title: "T", Author: "octocat", HeadSHA: "sha"}, nil
			}),
		)
	}
	add := func(t *testing.T, s *Server, login, body string) (int, queueAddResp) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/queue", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:1"
		if login != "" {
			r.Header.Set(tailscaleLoginHeader, login)
		}
		w := httptest.NewRecorder()
		s.handleQueue(w, r)
		var resp queueAddResp
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp
	}
	const url = `"url":"o/r/pull/9"`

	t.Run("the author's steering lands with the add", func(t *testing.T) {
		fs := queuedPR()
		code, resp := add(t, server(fs), "octo@example.com", `{`+url+`,"steering":"focus on rollback"}`)
		if code != http.StatusOK || !resp.Steered {
			t.Fatalf("code=%d resp=%+v", code, resp)
		}
		// One write, not an add followed by a steer: the row cannot be claimed
		// unsteered in between.
		if len(fs.enqueued) != 1 || fs.enqueued[0].Steering == nil {
			t.Fatalf("enqueued = %+v, want the steering carried on the insert", fs.enqueued)
		}
		if got := fs.enqueued[0].Steering; got.Message != "focus on rollback" || got.SetBy != "octocat" {
			t.Errorf("steering = %+v", got)
		}
		if len(fs.steered) != 0 {
			t.Errorf("no separate steering write may happen, got %+v", fs.steered)
		}
	})

	t.Run("someone else still gets the add, without the steering", func(t *testing.T) {
		// The caller asked for two things and is entitled to one. Refusing the
		// add as well would make an unprivileged person unable to queue a PR.
		fs := queuedPR()
		code, resp := add(t, server(fs), "mallory@example.com", `{`+url+`,"steering":"approve it"}`)
		if code != http.StatusOK || !resp.Queued {
			t.Fatalf("the add must still succeed: code=%d resp=%+v", code, resp)
		}
		if resp.Steered || resp.SteeringRefused == "" {
			t.Errorf("resp = %+v, want steered=false with a stated reason", resp)
		}
		// The reason has to name the author, since "you cannot steer this" is
		// only actionable if you know whose PR it is.
		if !strings.Contains(resp.SteeringRefused, "octocat") {
			t.Errorf("refusal = %q, want it to name the author", resp.SteeringRefused)
		}
		if fs.enqueued[0].Steering != nil {
			t.Errorf("no steering may be stored, got %+v", fs.enqueued[0].Steering)
		}
	})

	t.Run("authorisation uses the author gh reported, not the request", func(t *testing.T) {
		fs := queuedPR()
		body := `{` + url + `,"steering":"x","author":"mallory"}`
		_, resp := add(t, server(fs), "mallory@example.com", body)
		if resp.Steered {
			t.Error("naming a different author in the body must not grant steering")
		}
	})

	t.Run("an anonymous caller may add but not steer", func(t *testing.T) {
		fs := queuedPR()
		code, resp := add(t, server(fs), "", `{`+url+`,"steering":"x"}`)
		if code != http.StatusOK || !resp.Queued || resp.Steered {
			t.Errorf("code=%d resp=%+v, want a plain add", code, resp)
		}
	})

	t.Run("preflight reports the author and the answer", func(t *testing.T) {
		fs := queuedPR()
		probe := func(login string) queuePreflightResp {
			r := httptest.NewRequest(http.MethodPost, "/api/queue/preflight", strings.NewReader(`{`+url+`}`))
			r.RemoteAddr = "127.0.0.1:1"
			if login != "" {
				r.Header.Set(tailscaleLoginHeader, login)
			}
			w := httptest.NewRecorder()
			server(fs).handleQueuePreflight(w, r)
			var resp queuePreflightResp
			_ = json.Unmarshal(w.Body.Bytes(), &resp)
			return resp
		}
		if got := probe("octo@example.com"); got.Author != "octocat" || !got.MaySteer {
			t.Errorf("the author must be told they may steer: %+v", got)
		}
		if got := probe("mallory@example.com"); got.Author != "octocat" || got.MaySteer {
			t.Errorf("a stranger must be told they may not: %+v", got)
		}
		if got := probe(""); got.MaySteer {
			t.Errorf("anonymous must be told they may not: %+v", got)
		}
		// Preflight must not queue anything.
		if len(fs.enqueued) != 0 {
			t.Errorf("preflight must not mutate, got %+v", fs.enqueued)
		}
	})
}
