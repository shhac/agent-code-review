package usage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// liveUsagePayload is the shape the endpoint actually returned, trimmed to
// the fields this package reads plus a couple it must ignore.
const liveUsagePayload = `{
  "five_hour":  {"utilization": 3.0, "resets_at": "2026-07-28T00:20:00.663359+00:00"},
  "seven_day":  {"utilization": 8.0, "resets_at": "2026-08-02T15:00:00.663379+00:00"},
  "seven_day_opus": null,
  "extra_usage": {"is_enabled": true, "monthly_limit": 3750, "used_credits": 173.0},
  "limits": [{"kind": "session", "percent": 3}, {"kind": "weekly_all", "percent": 8}]
}`

func TestClaudeUsageMapsWindows(t *testing.T) {
	var gotAuth, gotBeta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotBeta = r.Header.Get("Authorization"), r.Header.Get("anthropic-beta")
		_, _ = w.Write([]byte(liveUsagePayload))
	}))
	defer srv.Close()

	snap, err := claudeUsage(context.Background(), srv.URL, "tok-abc")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok-abc" || gotBeta != claudeOAuthBeta {
		t.Errorf("headers: auth=%q beta=%q", gotAuth, gotBeta)
	}
	if snap.Primary == nil || snap.Primary.UsedPercent != 3 || snap.Primary.WindowMins != claudeSessionMins {
		t.Errorf("primary = %+v, want the 5h window", snap.Primary)
	}
	if snap.Secondary == nil || snap.Secondary.UsedPercent != 8 || snap.Secondary.WindowMins != claudeWeeklyMins {
		t.Errorf("secondary = %+v, want the weekly window", snap.Secondary)
	}
	if snap.Primary.ResetsAt == 0 || snap.Secondary.ResetsAt == 0 {
		t.Error("both windows must carry a parsed reset time")
	}
	if snap.FetchedAt.IsZero() {
		t.Error("FetchedAt must be stamped: BelowFloor treats a zero snapshot as no-opinion")
	}
}

// The two windows must land on the same side of BelowFloor's weekly split as
// codex's do, or the configured floors would apply to the wrong window.
func TestClaudeWindowsSatisfyTheFloorSplit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":95},"seven_day":{"utilization":1}}`))
	}))
	defer srv.Close()

	snap, err := claudeUsage(context.Background(), srv.URL, "t")
	if err != nil {
		t.Fatal(err)
	}
	paused, reason := BelowFloor(snap, 10, 10)
	if !paused || !strings.Contains(reason, "5h") {
		t.Errorf("a 95%%-used session window must trip the 5h floor, got paused=%v reason=%q", paused, reason)
	}
}

// Every failure path must fail open, and none may leak the bearer token: the
// error string is rendered on the dashboard.
func TestClaudeUsageErrorsAreSafeAndFailOpen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"expired"}`))
	}))
	defer srv.Close()

	snap, err := claudeUsage(context.Background(), srv.URL, "super-secret-token")
	if err == nil {
		t.Fatal("a 401 must surface as an error")
	}
	if strings.Contains(err.Error(), "super-secret-token") {
		t.Fatalf("error leaked the credential: %v", err)
	}
	if paused, _ := BelowFloor(snap, 10, 10); paused {
		t.Error("an errored snapshot must never pause reviews")
	}
}

func TestAccessTokenFrom(t *testing.T) {
	token, err := accessTokenFrom([]byte(`{"claudeAiOauth":{"accessToken":"abc123","expiresAt":1}}`))
	if err != nil || token != "abc123" {
		t.Errorf("token = %q, err = %v", token, err)
	}
	for name, blob := range map[string]string{
		"no token":     `{"claudeAiOauth":{"accessToken":""}}`,
		"wrong shape":  `{"other":{}}`,
		"not json":     `nonsense`,
		"empty object": `{}`,
	} {
		if _, err := accessTokenFrom([]byte(blob)); err == nil {
			t.Errorf("%s must fail", name)
		}
	}
}

// A missing window is normal (the account has no such limit); it must map to
// nil rather than a zero-percent window that would read as "plenty left".
func TestToClaudeWindowNilPassesThrough(t *testing.T) {
	if got := toClaudeWindow(nil, claudeWeeklyMins); got != nil {
		t.Errorf("nil window = %+v, want nil", got)
	}
}

// The file-store path is the one branch a machine without a keychain entry
// relies on, and it was previously reachable only through an integration test
// that skips whenever no real credential exists.
func TestCredentialFileToken(t *testing.T) {
	dir := t.TempDir()

	good := filepath.Join(dir, "creds.json")
	if err := os.WriteFile(good, []byte(`{"claudeAiOauth":{"accessToken":"tok-abc"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if token, err := credentialFileToken(good); err != nil || token != "tok-abc" {
		t.Errorf("token = %q, err = %v", token, err)
	}

	// A missing file must name the fix, not surface a raw ENOENT.
	_, err := credentialFileToken(filepath.Join(dir, "absent.json"))
	if err == nil || !strings.Contains(err.Error(), "claude auth login") {
		t.Errorf("missing file error = %v, want an actionable hint", err)
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := credentialFileToken(bad); err == nil {
		t.Error("a malformed credential file must fail")
	}
}
