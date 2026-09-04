package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

// TestFromLoopback pins the control that does not depend on configuration
// being right. The listener binds loopback, but a wider bind is one config
// edit away, and this is what makes such a bind degrade to "nobody is
// identified" rather than "everybody is whoever they say".
func TestFromLoopback(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:54321":     true,
		"[::1]:54321":         true,
		"127.0.0.1":           true,  // no port: some proxies report bare hosts
		"100.101.66.81:54321": false, // a tailnet peer reaching the port directly
		"10.61.55.98:8330":    false, // the LAN
		"203.0.113.7:443":     false, // the public internet
		"":                    false,
		"not-an-address":      false,
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/viewer", nil)
		r.RemoteAddr = addr
		if got := fromLoopback(r); got != want {
			t.Errorf("fromLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

// TestMaySteer pins the authorisation rule itself, apart from any request.
func TestMaySteer(t *testing.T) {
	author := viewer{Login: "o@e.com", Handle: "octocat"}
	operator := viewer{Login: "p@e.com", Handle: "paul-gh", IsGH: true}

	for name, tc := range map[string]struct {
		v      viewer
		author string
		want   bool
	}{
		"author steers their own":        {author, "octocat", true},
		"casing does not matter":         {author, "OctoCat", true},
		"author steers nobody else's":    {author, "someone-else", false},
		"operator steers any":            {operator, "someone-else", true},
		"anonymous steers nothing":       {viewer{}, "octocat", false},
		"unmapped steers nothing":        {viewer{Login: "x@e.com"}, "x@e.com", false},
		"an empty author matches nobody": {author, "", false},
	} {
		if got := tc.v.maySteer(tc.author); got != tc.want {
			t.Errorf("%s: maySteer(%q) = %v, want %v", name, tc.author, got, tc.want)
		}
	}
}
