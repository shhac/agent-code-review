package dashboard

import (
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
	return &Server{
		store:              fs,
		config:             func() config.Config { return config.Config{GHUser: "paul-gh"} },
		trustProxyIdentity: trust,
	}
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

// TestViewerState pins the classification the chip renders. Pure, so the four
// cases cost nothing; previously handleViewer had no test at all and the
// client re-derived these cases from a combination of booleans.
func TestViewerState(t *testing.T) {
	for name, tc := range map[string]struct {
		v    viewer
		want viewerState
	}{
		"nothing proved":        {viewer{}, viewerAnonymous},
		"proved, no roster row": {viewer{Login: "x@e.com"}, viewerUnmapped},
		"rostered author":       {viewer{Login: "o@e.com", Handle: "octocat"}, viewerAuthor},
		"the gh account":        {viewer{Login: "p@e.com", Handle: "paul-gh", IsGH: true}, viewerOperator},
	} {
		if got := tc.v.state(); got != tc.want {
			t.Errorf("%s: state = %q, want %q", name, got, tc.want)
		}
	}
}

// TestHandleViewer: the endpoint reports the identity it proved, and reports
// nobody when nothing was proved. It must never echo a handle it did not
// resolve, since the chip is what tells a person they were recognised.
func TestHandleViewer(t *testing.T) {
	get := func(t *testing.T, s *Server, remote, login string) viewerResp {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/viewer", nil)
		r.RemoteAddr = remote
		if login != "" {
			r.Header.Set(tailscaleLoginHeader, login)
		}
		w := httptest.NewRecorder()
		s.handleViewer(w, r)
		var resp viewerResp
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v (%s)", err, w.Body)
		}
		return resp
	}

	fs := queuedPR()
	t.Run("a proxied roster member is named", func(t *testing.T) {
		got := get(t, steerServer(fs, true), "127.0.0.1:1", "octo@example.com")
		if got.State != viewerAuthor || got.Handle != "octocat" || got.Login != "octo@example.com" {
			t.Errorf("viewer = %+v", got)
		}
	})
	t.Run("the gh account is the operator", func(t *testing.T) {
		if got := get(t, steerServer(fs, true), "127.0.0.1:1", "paul@example.com"); got.State != viewerOperator {
			t.Errorf("state = %q, want operator", got.State)
		}
	})
	t.Run("an unrostered login is unmapped, with no handle", func(t *testing.T) {
		got := get(t, steerServer(fs, true), "127.0.0.1:1", "stranger@example.com")
		if got.State != viewerUnmapped || got.Handle != "" {
			t.Errorf("viewer = %+v, want unmapped with no handle", got)
		}
	})
	t.Run("a direct connection is anonymous, whatever it claims", func(t *testing.T) {
		got := get(t, steerServer(fs, true), "100.64.0.1:1", "paul@example.com")
		if got.State != viewerAnonymous || got.Handle != "" || got.Login != "" {
			t.Errorf("viewer = %+v, want anonymous with nothing echoed back", got)
		}
	})
	t.Run("funnel mode is anonymous", func(t *testing.T) {
		if got := get(t, steerServer(fs, false), "127.0.0.1:1", "paul@example.com"); got.State != viewerAnonymous {
			t.Errorf("state = %q, want anonymous", got.State)
		}
	})
}
