package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	output "github.com/shhac/lib-agent-output"
)

// captureLog swaps the stderr sink for a buffer.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := stderrLog
	stderrLog = output.NewNDJSONWriter(&buf)
	t.Cleanup(func() { stderrLog = old })
	return &buf
}

// The daemon log's whole reason to change: a line has to say WHEN, so a log
// full of "skipping repo this cycle" can answer whether discovery stopped or
// is merely repeating itself.
func TestLogLineIsTimestampedNDJSON(t *testing.T) {
	buf := captureLog(t)
	before := time.Now().UTC().Truncate(time.Second)
	stderrLogf("discover: %d candidate(s) upserted", 2)

	var got logLine
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("log line is not JSON (%v): %s", err, buf.String())
	}
	ts, err := time.Parse(time.RFC3339, got.TS)
	if err != nil {
		t.Fatalf("ts %q is not RFC3339: %v", got.TS, err)
	}
	if ts.Before(before) || ts.After(time.Now().UTC().Add(time.Second)) {
		t.Errorf("ts %s is not around now (%s)", ts, before)
	}
	if got.Level != "info" {
		t.Errorf("level = %q, want info", got.Level)
	}
	if got.Msg != "discover: 2 candidate(s) upserted" {
		t.Errorf("msg = %q, want the formatted message", got.Msg)
	}
}

func TestWarnCarriesItsLevel(t *testing.T) {
	buf := captureLog(t)
	stderrWarnf("discover %s: in backoff", "o/a")

	var got logLine
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("log line is not JSON (%v): %s", err, buf.String())
	}
	if got.Level != "warn" {
		t.Errorf("level = %q, want warn", got.Level)
	}
}

// One record per line, so a tail is parseable a line at a time.
func TestLogLinesAreOnePerLine(t *testing.T) {
	buf := captureLog(t)
	stderrLogf("first")
	stderrWarnf("second")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), buf.String())
	}
	for i, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Errorf("line %d is not valid JSON: %s", i, line)
		}
	}
}
