package review

import (
	"bytes"
	"flag"
	"os"
	"strings"
	"testing"
	"time"
)

// fixedClock advances a fixed step per call so rendered durations are
// deterministic.
func fixedClock(step time.Duration) func() time.Time {
	base := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	n := 0
	return func() time.Time {
		n++
		return base.Add(time.Duration(n) * step)
	}
}

func transcode(t *testing.T, lines ...string) (string, *streamTranscoder) {
	t.Helper()
	var out bytes.Buffer
	tr := newStreamTranscoder(&out)
	tr.now = fixedClock(500 * time.Millisecond)
	for _, l := range lines {
		if _, err := tr.Write([]byte(l + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	tr.Close()
	return out.String(), tr
}

// TestTranscodeRendersMarkerFormat is the contract test between this driver
// and the dashboard parser: the rendered transcript must use the same bare
// marker lines codex prints, because one parser reads both.
func TestTranscodeRendersMarkerFormat(t *testing.T) {
	got, tr := transcode(t,
		`{"type":"system","subtype":"init","session_id":"abc-123"}`,
		`{"type":"user","message":{"content":[{"type":"text","text":"Review PR #7"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"checking the diff"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"gh pr diff 7"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"diff --git a/x"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"{\"decision\":\"APPROVED\",\"summary\":\"ok\"}"}]}}`,
		`{"type":"result","subtype":"success","session_id":"abc-123","structured_output":{"decision":"APPROVED","summary":"ok"},"usage":{"input_tokens":1000,"output_tokens":234}}`,
	)

	for _, want := range []string{
		"session id: abc-123",
		"user\nReview PR #7",
		"thinking\nchecking the diff",
		"exec\ngh pr diff 7",
		" succeeded in 500ms:\ndiff --git a/x",
		"claude\n{\"decision\":\"APPROVED\",\"summary\":\"ok\"}",
		"tokens used\n1,234",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript missing %q\n--- got ---\n%s", want, got)
		}
	}
	if tr.sessionID != "abc-123" {
		t.Errorf("sessionID = %q", tr.sessionID)
	}
	if tr.tokens != 1234 {
		t.Errorf("tokens = %d, want every usage field summed", tr.tokens)
	}
}

// The rendered exec result line must satisfy the regex the UI parser closes
// exec blocks with: /^ (succeeded|exited|failed)\b.*?(?: in ([^\s:]+))?:?\s*$/
func TestTranscodeExecResultLineShape(t *testing.T) {
	got, _ := transcode(t,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"false"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"boom","is_error":true}]}}`,
	)
	if !strings.Contains(got, " failed in 500ms:\nboom") {
		t.Errorf("an errored tool result must render as a failed exec:\n%s", got)
	}
}

// Only the first text-bearing user message is the prompt; later ones are tool
// results and must not re-open a `user` block.
func TestTranscodeEmitsPromptOnce(t *testing.T) {
	got, _ := transcode(t,
		`{"type":"user","message":{"content":[{"type":"text","text":"the prompt"}]}}`,
		`{"type":"user","message":{"content":[{"type":"text","text":"not the prompt"}]}}`,
	)
	if n := strings.Count(got, "\nuser\n") + strings.Count(got, "user\nthe prompt"); n != 1 {
		t.Errorf("want exactly one user block, got:\n%s", got)
	}
	if strings.Contains(got, "not the prompt") {
		t.Errorf("a later text-bearing user message must not render as a prompt:\n%s", got)
	}
}

// Non-Bash tools render as name + arguments so the exec block still says what
// ran.
func TestTranscodeRendersNonBashTool(t *testing.T) {
	got, _ := transcode(t,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/a/b.go"}}]}}`,
	)
	if !strings.Contains(got, `exec`+"\n"+`Read {"file_path":"/a/b.go"}`) {
		t.Errorf("non-Bash tool call must render name + input:\n%s", got)
	}
}

// The subprocess hands over arbitrary chunks, so an event split mid-line must
// still render exactly once.
func TestTranscodeReassemblesSplitLines(t *testing.T) {
	var out bytes.Buffer
	tr := newStreamTranscoder(&out)
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}` + "\n"
	if _, err := tr.Write([]byte(line[:30])); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "hello") {
		t.Error("a partial line must not render until it is complete")
	}
	if _, err := tr.Write([]byte(line[30:])); err != nil {
		t.Fatal(err)
	}
	tr.Close()
	if n := strings.Count(out.String(), "hello"); n != 1 {
		t.Errorf("split line rendered %d times, want 1:\n%s", n, out.String())
	}
}

// A stream line that isn't JSON is diagnostics; it must survive into the log
// rather than be dropped.
func TestTranscodePassesThroughNonJSON(t *testing.T) {
	got, _ := transcode(t, `warning: something happened`)
	if !strings.Contains(got, "warning: something happened") {
		t.Errorf("non-JSON output must reach the log:\n%s", got)
	}
}

// An unrecognised event type must render nothing and break nothing: the
// protocol grows, and a review must not fail because of a new event.
func TestTranscodeIgnoresUnknownEvents(t *testing.T) {
	got, tr := transcode(t,
		`{"type":"prompt_suggestion","suggestion":"try this"}`,
		`{"type":"result","subtype":"success","structured_output":{"decision":"SKIPPED","summary":"s"},"usage":{"input_tokens":5,"output_tokens":0}}`,
	)
	if strings.Contains(got, "try this") {
		t.Errorf("unknown events must not render:\n%s", got)
	}
	v, err := tr.verdict()
	if err != nil || v.Decision != DecisionSkipped {
		t.Errorf("verdict = %+v, err = %v", v, err)
	}
}

func TestWithThousands(t *testing.T) {
	for in, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 192575: "192,575", 1234567: "1,234,567"} {
		if got := withThousands(in); got != want {
			t.Errorf("withThousands(%d) = %q, want %q", in, got, want)
		}
	}
}

// goldenTranscript is the fixture both sides of the log contract read: this
// package writes it, and the dashboard's parser test
// (internal/dashboard/ui/src/lib/agentlog.test.ts) parses it. Regenerate with
// `go test ./internal/review -update-golden`.
const goldenTranscript = "testdata/claude-transcript.golden"

var updateGolden = flag.Bool("update-golden", false, "rewrite the cross-language transcript fixture")

// TestTranscodeGoldenTranscript pins the exact bytes the claude driver tees
// into agent.log. Rendering here and parsing in TypeScript is the one seam
// the "single agent-log format" design rests on, so a change to either side
// that breaks the other must fail a test rather than silently degrade the
// review log to a raw JSON dump.
// The event sequence mirrors a real `claude -p --json-schema` run as observed
// live: the prompt is never echoed as a stream event (the driver renders it),
// and the agent reports by calling the StructuredOutput tool rather than
// writing a final message, so the report reaches the log via the result event.
func TestTranscodeGoldenTranscript(t *testing.T) {
	var out bytes.Buffer
	tr := newStreamTranscoder(&out)
	tr.now = fixedClock(500 * time.Millisecond)
	tr.userPrompt("Review pull request owner/repo#42.")
	for _, line := range []string{
		`{"type":"system","subtype":"init","session_id":"9f1c2d3e-0000-4444-8888-abcdefabcdef"}`,
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"Read the diff, then check it against the linked issue."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"gh pr diff 42"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"diff --git a/main.go b/main.go\n+ added a line"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"/tmp/wd/notes.md"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"no such file","is_error":true}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t3","name":"StructuredOutput","input":{"decision":"COMMENTED","summary":"Left two inline notes about error handling."}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t3","content":"Structured output provided successfully"}]}}`,
		`{"type":"result","subtype":"success","session_id":"9f1c2d3e-0000-4444-8888-abcdefabcdef","structured_output":{"decision":"COMMENTED","summary":"Left two inline notes about error handling."},"usage":{"input_tokens":12000,"output_tokens":800,"cache_read_input_tokens":30000},"total_cost_usd":0.6231}`,
	} {
		if _, err := tr.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	tr.Close()
	got := out.String()

	if *updateGolden {
		if err := os.WriteFile(goldenTranscript, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", goldenTranscript)
		return
	}
	want, err := os.ReadFile(goldenTranscript)
	if err != nil {
		t.Fatalf("%v (regenerate with: go test ./internal/review -update-golden)", err)
	}
	if got != string(want) {
		t.Errorf("transcript drifted from the fixture the UI parser reads.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// Cost is the only per-review spend signal an engine reports, so it has to
// survive resumes the same way tokens do.
func TestTranscodeSumsCostAcrossInvocations(t *testing.T) {
	got, tr := transcode(t,
		`{"type":"result","subtype":"success","structured_output":{"decision":"WORKING","summary":"w"},"usage":{"input_tokens":10},"total_cost_usd":0.25}`,
		`{"type":"result","subtype":"success","structured_output":{"decision":"COMMENTED","summary":"c"},"usage":{"input_tokens":10},"total_cost_usd":0.5}`,
	)
	if tr.costUSD != 0.75 {
		t.Errorf("costUSD = %v, want both invocations summed", tr.costUSD)
	}
	if !strings.Contains(got, "~ $0.7500 at API rates") {
		t.Errorf("transcript must show the running cost:\n%s", got)
	}
}

// codex reports no cost, so a zero must render nothing rather than an
// authoritative-looking "$0.0000".
func TestTranscodeOmitsCostWhenUnreported(t *testing.T) {
	got, tr := transcode(t,
		`{"type":"result","subtype":"success","structured_output":{"decision":"SKIPPED","summary":"s"},"usage":{"input_tokens":5}}`,
	)
	if tr.costUSD != 0 {
		t.Errorf("costUSD = %v, want 0", tr.costUSD)
	}
	if strings.Contains(got, "API rates") {
		t.Errorf("an unreported cost must not render:\n%s", got)
	}
}

// The CLI varies these wire shapes: a message's content is usually an array of
// blocks but can be a bare string, and a tool result can be a string, an array
// of parts, or something this code has no schema for. Only the common shapes
// were driven, so the fallbacks were untested — and a regression there
// degrades the transcript the dashboard and `queue log -f` both render.
func TestTranscodeHandlesWireShapeVariance(t *testing.T) {
	// A bare-string user message still counts as the prompt.
	got, _ := transcode(t, `{"type":"user","message":{"content":"a bare string prompt"}}`)
	if !strings.Contains(got, "user\na bare string prompt") {
		t.Errorf("bare-string message content must render:\n%s", got)
	}

	// A tool result delivered as parts is joined, not dropped.
	got, _ = transcode(t,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}]}}`,
	)
	if !strings.Contains(got, "first\nsecond") {
		t.Errorf("multi-part tool result must be joined:\n%s", got)
	}

	// An unrecognised result shape falls back to its raw form rather than
	// vanishing: an unreadable transcript beats a silently empty one.
	got, _ = transcode(t,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"x"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":{"unexpected":"shape"}}]}}`,
	)
	if !strings.Contains(got, "unexpected") {
		t.Errorf("unknown tool-result shape must fall back to raw:\n%s", got)
	}
}

// A run that ends without a report must say WHY in the transcript. Review
// 21212 failed three seconds in and its log read only "tokens used 0",
// because the driver captured structured_output and usage and discarded
// is_error, subtype, and result — dropping the diagnosis exactly when it was
// the only thing worth having.
func TestTranscodeRendersFailureReason(t *testing.T) {
	got, tr := transcode(t,
		`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"Claude Code process exited with code 1","usage":{"input_tokens":0}}`,
	)
	if !strings.Contains(got, "error\nerror_during_execution: Claude Code process exited with code 1") {
		t.Errorf("failure must render under the error marker:\n%s", got)
	}
	// And the driver's error names it, rather than saying "no structured output".
	if _, err := tr.verdict(); err == nil || !strings.Contains(err.Error(), "exited with code 1") {
		t.Errorf("verdict error = %v, want the CLI's own reason", err)
	}
}

// A successful run must stay silent: an error bubble on every review would
// train the reader to ignore it.
func TestTranscodeRendersNoFailureOnSuccess(t *testing.T) {
	got, _ := transcode(t,
		`{"type":"result","subtype":"success","structured_output":{"decision":"APPROVED","summary":"ok"},"usage":{"input_tokens":10}}`,
	)
	if strings.Contains(got, "\nerror\n") {
		t.Errorf("a successful run must not render a failure:\n%s", got)
	}
}

// The shape review 21212 actually produced: no structured output, no error
// flag, nothing else to go on. It must still say something.
func TestTranscodeRendersBareMissingReport(t *testing.T) {
	got, _ := transcode(t, `{"type":"result","subtype":"success","usage":{"input_tokens":0}}`)
	if !strings.Contains(got, "error\nthe run ended without a report") {
		t.Errorf("a report-less run must still explain itself:\n%s", got)
	}
}
