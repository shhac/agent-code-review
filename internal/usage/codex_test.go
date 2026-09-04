package usage

// Tests for the codex headroom reader in codex.go, kept beside it the way
// claude_test.go sits beside claude.go. usage_test.go covers only what both
// engines share.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeCodex(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex")
	// The trailing drain keeps the fake alive on stdin until the parent closes
	// it. fetchCodex writes the 3-message handshake BEFORE reading a reply, so
	// a fake that printed its answer and exited raced the writer: when the
	// child won, the write failed with EPIPE and fetchCodex returned that
	// instead of the answer already sitting in the pipe. It went red on CI
	// (a loaded runner schedules the child sooner) and never locally.
	//
	// A body that exits deliberately returns before reaching the drain, so the
	// failure fakes still exit as written.
	script := "#!/bin/sh\n" + body + "\ncat >/dev/null\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestWriteHandshake pins the request half of the app-server protocol: the
// exact method names, id sequence, and one-JSON-object-per-line framing the
// desktop-app contract depends on. Without this pin a typo in a method name
// passes every test (the fake-codex scripts ignore stdin).
func TestWriteHandshake(t *testing.T) {
	var buf strings.Builder
	if err := writeHandshake(&buf); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("handshake must be 3 newline-framed messages, got %d: %q", len(lines), buf.String())
	}
	type msg struct {
		JSONRPC string         `json:"jsonrpc"`
		ID      *int           `json:"id"`
		Method  string         `json:"method"`
		Params  map[string]any `json:"params"`
	}
	decode := func(line string) msg {
		t.Helper()
		var m msg
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("handshake line is not JSON: %q (%v)", line, err)
		}
		if m.JSONRPC != "2.0" {
			t.Errorf("jsonrpc = %q, want 2.0 in %q", m.JSONRPC, line)
		}
		return m
	}

	initMsg := decode(lines[0])
	if initMsg.Method != "initialize" || initMsg.ID == nil || *initMsg.ID != 1 {
		t.Errorf("first message must be initialize with id 1, got %+v", initMsg)
	}
	if ci, ok := initMsg.Params["clientInfo"].(map[string]any); !ok || ci["name"] != "agent-code-review" {
		t.Errorf("initialize must carry clientInfo.name, got %v", initMsg.Params)
	}

	notify := decode(lines[1])
	if notify.Method != "initialized" || notify.ID != nil {
		t.Errorf("second message must be the id-less initialized notification, got %+v", notify)
	}

	read := decode(lines[2])
	if read.Method != "account/rateLimits/read" || read.ID == nil || *read.ID != rateLimitsRequestID {
		t.Errorf("third message must read rate limits with the id parseRateLimits scans for, got %+v", read)
	}
}

func TestFetchReadsAppServerRateLimits(t *testing.T) {
	bin := fakeCodex(t, `printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"rateLimits":{"planType":"pro","primary":{"usedPercent":25,"windowDurationMins":300,"resetsAt":123},"secondary":{"usedPercent":50,"windowDurationMins":10080,"resetsAt":456}}}}'`)
	snap, err := Fetch(context.Background(), Source{Engine: "codex", Bin: bin})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Plan != "pro" || snap.Primary == nil || snap.Primary.UsedPercent != 25 || snap.Secondary == nil || snap.Secondary.WindowMins != 10080 {
		t.Errorf("snapshot = %+v", snap)
	}
}

func TestParseRateLimits(t *testing.T) {
	// A realistic stream: init response (id=1), a notification, then the
	// rate-limit response (id=2), captured shape from codex 0.138.0.
	stream := `{"id":1,"result":{"userAgent":"x"}}
{"jsonrpc":"2.0","method":"some/notification","params":{}}
not even json
{"id":2,"result":{"rateLimits":{"planType":"prolite","primary":{"usedPercent":46,"windowDurationMins":300,"resetsAt":1783438334},"secondary":{"usedPercent":7,"windowDurationMins":10080,"resetsAt":1784025134}}}}
`
	snap, err := parseRateLimits(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Plan != "prolite" {
		t.Errorf("plan = %q", snap.Plan)
	}
	if snap.Primary == nil || snap.Primary.UsedPercent != 46 || snap.Primary.WindowMins != 300 {
		t.Errorf("primary = %+v", snap.Primary)
	}
	if snap.Secondary == nil || snap.Secondary.WindowMins != 10080 {
		t.Errorf("secondary = %+v", snap.Secondary)
	}
	if snap.FetchedAt.IsZero() {
		t.Error("FetchedAt must be stamped")
	}
}

func TestParseRateLimitsErrorAndEOF(t *testing.T) {
	if _, err := parseRateLimits(strings.NewReader(`{"id":2,"error":{"message":"nope"}}` + "\n")); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("rpc error must surface, got %v", err)
	}
	if _, err := parseRateLimits(strings.NewReader(`{"id":1,"result":{}}` + "\n")); err == nil {
		t.Error("stream ending without id=2 must error")
	}
	// Missing windows → nil pointers, no panic.
	snap, err := parseRateLimits(strings.NewReader(`{"id":2,"result":{"rateLimits":{"planType":"x"}}}` + "\n"))
	if err != nil || snap.Primary != nil || snap.Secondary != nil {
		t.Errorf("windowless snapshot mishandled: %+v err=%v", snap, err)
	}
}
