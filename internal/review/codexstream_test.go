package review

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// transcodeCodex drives a canned event stream through the transcoder with a
// clock that advances 500ms per reading, so rendered durations are stable.
func transcodeCodex(t *testing.T, lines ...string) (string, *codexTranscoder) {
	t.Helper()
	var out strings.Builder
	tr := newCodexTranscoder(&out)
	tick := time.Unix(0, 0)
	tr.now = func() time.Time {
		tick = tick.Add(500 * time.Millisecond)
		return tick
	}
	for _, l := range lines {
		if _, err := tr.Write([]byte(l + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	tr.Close()
	return out.String(), tr
}

// The transcript must come out in the same bare-marker format claude's
// transcoder produces and codex used to print itself, because one dashboard
// parser serves both engines. Switching codex to --json made this file
// responsible for a rendering that used to be free.
func TestCodexTranscodeRendersMarkerFormat(t *testing.T) {
	got, tr := transcodeCodex(t,
		`{"type":"thread.started","thread_id":"019fa991-ec65-7ba0-b058-47e2eac11a4f"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"i0","type":"error","message":"skill descriptions were shortened"}}`,
		`{"type":"item.started","item":{"id":"i1","type":"command_execution","command":"gh pr diff 7"}}`,
		`{"type":"item.completed","item":{"id":"i1","type":"command_execution","command":"gh pr diff 7","aggregated_output":"diff --git a/x","exit_code":0}}`,
		`{"type":"item.completed","item":{"id":"i2","type":"agent_message","text":"{\"decision\":\"APPROVED\",\"summary\":\"ok\"}"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1000,"cached_input_tokens":400,"output_tokens":234,"reasoning_output_tokens":30}}`,
	)

	for _, want := range []string{
		"session id: 019fa991-ec65-7ba0-b058-47e2eac11a4f",
		"error\nskill descriptions were shortened",
		"exec\ngh pr diff 7",
		" succeeded in 500ms:\ndiff --git a/x",
		"codex\n{\"decision\":\"APPROVED\",\"summary\":\"ok\"}",
		"tokens used\n834",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript missing %q\n--- got ---\n%s", want, got)
		}
	}
	if tr.threadID != "019fa991-ec65-7ba0-b058-47e2eac11a4f" {
		t.Errorf("threadID = %q", tr.threadID)
	}
}

// codex's input_tokens INCLUDES cached reads, so the fresh input is the
// difference. Measured: a run reporting input 18,389 / cached 10,496 printed a
// prose trailer of 7,928, which is input-cached+output, not input+output.
// Getting this backwards would double-count every cached read and inflate the
// one figure the cross-engine chart depends on.
func TestCodexUsageTreatsInputAsCacheInclusive(t *testing.T) {
	_, tr := transcodeCodex(t,
		`{"type":"turn.completed","usage":{"input_tokens":18389,"cached_input_tokens":10496,"output_tokens":209,"reasoning_output_tokens":32}}`,
	)
	want := TokenUsage{Input: 7893, Output: 209, CacheRead: 10496, Reasoning: 32}
	if tr.usage != want {
		t.Errorf("usage = %+v, want %+v", tr.usage, want)
	}
	// Fresh excludes the cached read; reasoning is inside output, not added.
	if got := tr.usage.Fresh(); got != 8102 {
		t.Errorf("fresh = %d, want 8102 (input-cached + output, reasoning not added)", got)
	}
	if got := tr.usage.Total(); got != 18598 {
		t.Errorf("total = %d, want 18598", got)
	}
}

// The regression that shipped: codex reports the SESSION total on every turn,
// so a resumed run's figure already contains the first invocation's. The old
// driver summed the prose trailers and double-counted every resumed review.
//
// Measured across three turns of one session: output 209 -> 230 -> 251,
// cached rising by exactly 18,176 each time, reasoning pinned at 32. Summing
// those would report output 690 for a session that produced 251.
func TestCodexUsageReplacesRatherThanSums(t *testing.T) {
	_, tr := transcodeCodex(t,
		`{"type":"turn.completed","usage":{"input_tokens":37139,"cached_input_tokens":18176,"output_tokens":209,"reasoning_output_tokens":32}}`,
		`{"type":"turn.completed","usage":{"input_tokens":55862,"cached_input_tokens":36352,"output_tokens":230,"reasoning_output_tokens":32}}`,
		`{"type":"turn.completed","usage":{"input_tokens":74619,"cached_input_tokens":54528,"output_tokens":251,"reasoning_output_tokens":32}}`,
	)
	want := TokenUsage{Input: 20091, Output: 251, CacheRead: 54528, Reasoning: 32}
	if tr.usage != want {
		t.Errorf("usage = %+v, want the LAST turn's cumulative figure %+v", tr.usage, want)
	}
}

// Every usage payload survives verbatim, so a pricing question about a field
// this driver never modelled stays a query rather than a migration.
func TestCodexKeepsRawUsagePayloads(t *testing.T) {
	_, tr := transcodeCodex(t,
		`{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":0,"output_tokens":2,"service_tier":"flex"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":20,"cached_input_tokens":5,"output_tokens":3,"service_tier":"flex"}}`,
	)
	raw := joinRawUsage(tr.rawUsage)
	var payloads []map[string]any
	if err := json.Unmarshal([]byte(raw), &payloads); err != nil {
		t.Fatalf("raw usage must be a JSON array: %v (%s)", err, raw)
	}
	if len(payloads) != 2 {
		t.Fatalf("kept %d payloads, want one per invocation", len(payloads))
	}
	// An unmodelled field is the whole point of keeping the payload.
	if payloads[1]["service_tier"] != "flex" {
		t.Errorf("unmodelled field lost: %+v", payloads[1])
	}
}

// A command whose completion arrives with no matching start still needs its
// command line rendered, or its output would attach to whatever ran before it.
func TestCodexCommandWithoutStartStillRenders(t *testing.T) {
	got, _ := transcodeCodex(t,
		`{"type":"item.completed","item":{"id":"solo","type":"command_execution","command":"ls -1","aggregated_output":"a\nb","exit_code":2}}`,
	)
	if !strings.Contains(got, "exec\nls -1") {
		t.Errorf("command line missing:\n%s", got)
	}
	if !strings.Contains(got, " failed:\na\nb") {
		t.Errorf("non-zero exit must render as failed:\n%s", got)
	}
}

// A line that isn't an event is passed through rather than dropped: codex
// prints the occasional plain warning and the log is the only record of it.
func TestCodexPassesThroughNonEventLines(t *testing.T) {
	got, _ := transcodeCodex(t, `warning: something unstructured`)
	if !strings.Contains(got, "warning: something unstructured") {
		t.Errorf("non-JSON line dropped:\n%s", got)
	}
}

// goldenCodexTranscript is the codex half of the cross-language log contract:
// this package writes it, and the dashboard's parser test
// (internal/dashboard/ui/src/lib/agentlog.test.ts) parses it. Regenerate with
// `go test ./internal/review -update-golden`.
const goldenCodexTranscript = "testdata/codex-transcript.golden"

// TestCodexTranscodeGoldenTranscript pins the exact bytes the codex driver
// tees into agent.log. Under --json that rendering is ours rather than
// codex's, so the guarantee that the dashboard can still read a codex log has
// to be a test rather than an assumption. The event sequence mirrors a real
// `codex exec --json` run as observed live.
func TestCodexTranscodeGoldenTranscript(t *testing.T) {
	var out strings.Builder
	tr := newCodexTranscoder(&out)
	tick := time.Unix(0, 0)
	tr.now = func() time.Time { tick = tick.Add(500 * time.Millisecond); return tick }
	tr.userPrompt("Review pull request owner/repo#42.")
	for _, line := range []string{
		`{"type":"thread.started","thread_id":"019f6f77-3c3d-7ce3-966d-d4b2083f4459"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.started","item":{"id":"i1","type":"command_execution","command":"gh pr diff 42"}}`,
		`{"type":"item.completed","item":{"id":"i1","type":"command_execution","command":"gh pr diff 42","aggregated_output":"diff --git a/main.go b/main.go\n+ added a line","exit_code":0}}`,
		`{"type":"item.started","item":{"id":"i2","type":"command_execution","command":"cat /tmp/wd/notes.md"}}`,
		`{"type":"item.completed","item":{"id":"i2","type":"command_execution","command":"cat /tmp/wd/notes.md","aggregated_output":"no such file","exit_code":1}}`,
		`{"type":"item.completed","item":{"id":"i3","type":"agent_message","text":"{\"decision\":\"COMMENTED\",\"summary\":\"Left two inline notes about error handling.\"}"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":42000,"cached_input_tokens":30000,"output_tokens":800,"reasoning_output_tokens":120}}`,
	} {
		if _, err := tr.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	tr.Close()
	got := out.String()

	if *updateGolden {
		if err := os.WriteFile(goldenCodexTranscript, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", goldenCodexTranscript)
		return
	}
	want, err := os.ReadFile(goldenCodexTranscript)
	if err != nil {
		t.Fatalf("%v (regenerate with: go test ./internal/review -update-golden)", err)
	}
	if got != string(want) {
		t.Errorf("transcript drifted from the fixture the UI parser reads.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
