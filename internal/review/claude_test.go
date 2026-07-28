package review

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
)

// argValue returns the value following flag, and whether it was present.
func argValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func hasArg(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// TestClaudeBuildArgs pins the CLI contract without a live claude run, the
// way TestCodexBuildArgs does for codex.
func TestClaudeBuildArgs(t *testing.T) {
	budget := 2.5
	e := newClaude(config.ClaudeSettings{
		Model: "opus", Effort: "high", PermissionMode: "dontAsk",
		AllowedTools: []string{"Bash", "Read"}, MaxBudgetUSD: budget,
		Args: []string{"--strict-mcp-config"},
	}, "keep going")
	args := e.buildArgs("REVIEW THIS")

	if !hasArg(args, "-p") || !hasArg(args, "--verbose") {
		t.Errorf("print + verbose are required for a parseable stream: %v", args)
	}
	if got, _ := argValue(args, "--output-format"); got != "stream-json" {
		t.Errorf("--output-format = %q, want stream-json", got)
	}
	for flag, want := range map[string]string{
		"--model": "opus", "--effort": "high", "--permission-mode": "dontAsk",
		"--allowedTools": "Bash,Read", "--max-budget-usd": "2.5",
	} {
		if got, ok := argValue(args, flag); !ok || got != want {
			t.Errorf("%s = %q (present=%v), want %q", flag, got, ok, want)
		}
	}
	if !hasArg(args, "--strict-mcp-config") {
		t.Errorf("configured extra args must be forwarded: %v", args)
	}

	// The schema travels inline, not as a path, and must be valid JSON on one
	// argv entry.
	schema, ok := argValue(args, "--json-schema")
	if !ok {
		t.Fatalf("--json-schema missing: %v", args)
	}
	if strings.Contains(schema, "\n") {
		t.Errorf("inline schema must be compacted onto one line, got %q", schema)
	}
	if !json.Valid([]byte(schema)) {
		t.Errorf("inline schema is not valid JSON: %q", schema)
	}

	// The prompt is last, and carries the shared reporting instruction.
	last := args[len(args)-1]
	if !strings.HasPrefix(last, "REVIEW THIS") || !strings.Contains(last, "Never end on WORKING.") {
		t.Errorf("final arg must be prompt+reportingInstruction, got %q", last)
	}
}

func TestClaudeBuildArgsOmitsUnsetOptionals(t *testing.T) {
	args := newClaude(config.ClaudeSettings{}, "nudge").buildArgs("p")
	if hasArg(args, "--max-budget-usd") {
		t.Errorf("--max-budget-usd must be omitted when unset: %v", args)
	}
	// Mode, model, and effort always ship: a headless run cannot answer a
	// prompt, and both dials are pinned rather than inherited from the
	// account so a review's depth and cost cannot drift under us.
	for _, tc := range []struct{ flag, want string }{
		{"--permission-mode", defaultPermissionMode},
		{"--model", defaultModel},
		{"--effort", defaultEffort},
	} {
		if got, _ := argValue(args, tc.flag); got != tc.want {
			t.Errorf("%s = %q, want the %q default", tc.flag, got, tc.want)
		}
	}
}

// An unpinned effort would also be an unrecorded one: the run reports no
// effort back, so Provenance is the only place it can come from.
func TestClaudeProvenanceCarriesPinnedEffort(t *testing.T) {
	if got := newClaude(config.ClaudeSettings{}, "n").effort; got != defaultEffort {
		t.Errorf("effort = %q, want %q so history can correlate cost with effort", got, defaultEffort)
	}
	if got := newClaude(config.ClaudeSettings{Effort: "xhigh"}, "n").effort; got != "xhigh" {
		t.Errorf("an explicit effort must win, got %q", got)
	}
}

// The pinned default must stay a model auto mode accepts, since auto is this
// engine's default permission mode: pairing them wrongly would fail every
// review. Haiku and the pre-4.6 models are the unsupported ones.
func TestClaudeDefaultModelSupportsAutoMode(t *testing.T) {
	for _, unsupported := range []string{"haiku", "claude-haiku", "claude-3", "sonnet-4-5", "opus-4-5"} {
		if strings.Contains(defaultModel, unsupported) {
			t.Errorf("defaultModel %q is not supported in %q permission mode", defaultModel, autoPermissionMode)
		}
	}
}

// An explicit model overrides the pin, including down to a model auto mode
// cannot use: that pairing is the user's to make (and to pair with a static
// permission mode).
func TestClaudeExplicitModelWins(t *testing.T) {
	args := newClaude(config.ClaudeSettings{Model: "haiku"}, "nudge").buildArgs("p")
	if got, _ := argValue(args, "--model"); got != "haiku" {
		t.Errorf("--model = %q, want the configured value", got)
	}
}

// In auto mode the classifier decides, so shipping a static allow-list would
// route commands around it. Allow rules resolve BEFORE the classifier, so
// `Bash(gh *)` there would exempt the one command that can merge and close.
func TestClaudeAutoModeShipsNoAllowList(t *testing.T) {
	args := newClaude(config.ClaudeSettings{}, "nudge").buildArgs("p")
	if got, _ := argValue(args, "--permission-mode"); got != autoPermissionMode {
		t.Errorf("--permission-mode = %q, want %q by default", got, autoPermissionMode)
	}
	if hasArg(args, "--allowedTools") {
		t.Errorf("auto mode must not pre-approve tools around the classifier: %v", args)
	}
}

// The static modes cannot reach gh on their own, so they keep the floor:
// without it the very first `gh pr view` aborts the run.
func TestClaudeStaticModesFallBackToGHAllowList(t *testing.T) {
	for _, mode := range []string{"acceptEdits", "dontAsk"} {
		args := newClaude(config.ClaudeSettings{PermissionMode: mode}, "nudge").buildArgs("p")
		tools, ok := argValue(args, "--allowedTools")
		if !ok {
			t.Errorf("%s: --allowedTools must ship: %v", mode, args)
			continue
		}
		if !strings.Contains(tools, "Bash(gh *)") {
			t.Errorf("%s: --allowedTools = %q, must permit gh", mode, tools)
		}
	}
}

// An explicit list replaces the fallback rather than extending it, so a
// deliberately tight config stays tight, and it is honoured in auto mode too
// (an opt-in fast path is the user's call to make).
func TestClaudeExplicitAllowedToolsWin(t *testing.T) {
	for _, mode := range []string{"", "acceptEdits", autoPermissionMode} {
		args := newClaude(config.ClaudeSettings{PermissionMode: mode, AllowedTools: []string{"Read"}}, "nudge").buildArgs("p")
		if got, _ := argValue(args, "--allowedTools"); got != "Read" {
			t.Errorf("mode %q: --allowedTools = %q, want only the configured list", mode, got)
		}
	}
}

func TestClaudeBuildResumeArgsCarriesSessionAndNudge(t *testing.T) {
	e := newClaude(config.ClaudeSettings{Model: "sonnet"}, "finish up")
	args := e.buildResumeArgs("sess-123")
	if got, _ := argValue(args, "--resume"); got != "sess-123" {
		t.Errorf("--resume = %q, want sess-123", got)
	}
	if got, _ := argValue(args, "--model"); got != "sonnet" {
		t.Errorf("resume must keep the pinned model, got %q", got)
	}
	if last := args[len(args)-1]; last != "finish up" {
		t.Errorf("resume prompt must be the final arg, got %q", last)
	}
}

// resultLine builds a claude result event carrying a structured report.
func resultLine(t *testing.T, sessionID, decision string, tokens int) string {
	t.Helper()
	ev := map[string]any{
		"type": "result", "subtype": "success", "session_id": sessionID,
		"structured_output": map[string]string{"decision": decision, "summary": "did the thing"},
		"usage":             map[string]int{"input_tokens": tokens, "output_tokens": 0},
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

// fakeClaude drives the engine off canned streams: one per invocation, in
// order. It records the args it was called with so the resume path is
// observable.
func fakeClaude(e *claudeEngine, streams ...string) *[][]string {
	calls := &[][]string{}
	e.runCmd = func(_ context.Context, args []string, _ string, stream, _ io.Writer) error {
		i := len(*calls)
		*calls = append(*calls, args)
		if i < len(streams) {
			_, _ = io.WriteString(stream, streams[i])
		}
		return nil
	}
	return calls
}

func TestClaudeReviewReadsStructuredReport(t *testing.T) {
	e := newClaude(config.ClaudeSettings{}, "nudge")
	fakeClaude(e, resultLine(t, "s1", DecisionApproved, 120))

	v, err := e.Review(context.Background(), Request{Prompt: "go", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if v.Decision != DecisionApproved || v.Summary != "did the thing" {
		t.Errorf("verdict = %+v", v)
	}
	if v.TokensUsed != 120 {
		t.Errorf("TokensUsed = %d, want 120", v.TokensUsed)
	}
}

// A run whose report is WORKING is resumed against its own session, and the
// tokens of every invocation add up, mirroring codex's trailer summing.
func TestClaudeReviewResumesOnWorkingReport(t *testing.T) {
	e := newClaude(config.ClaudeSettings{}, "finish up")
	calls := fakeClaude(e,
		resultLine(t, "s1", DecisionWorking, 100),
		resultLine(t, "s1", DecisionCommented, 50),
	)

	v, err := e.Review(context.Background(), Request{Prompt: "go", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if v.Decision != DecisionCommented {
		t.Errorf("decision = %q, want the resumed run's outcome", v.Decision)
	}
	if v.TokensUsed != 150 {
		t.Errorf("TokensUsed = %d, want both invocations summed", v.TokensUsed)
	}
	if len(*calls) != 2 {
		t.Fatalf("want 2 invocations, got %d", len(*calls))
	}
	if got, _ := argValue((*calls)[1], "--resume"); got != "s1" {
		t.Errorf("second invocation must resume s1, got %q", got)
	}
}

func TestClaudeReviewStopsResumingAtTheCap(t *testing.T) {
	zero := 0
	e := newClaude(config.ClaudeSettings{MaxResumes: &zero}, "nudge")
	calls := fakeClaude(e, resultLine(t, "s1", DecisionWorking, 10))

	v, err := e.Review(context.Background(), Request{Prompt: "go", WorkDir: t.TempDir()})
	if err == nil {
		t.Fatal("a run that never reports an outcome must surface an error")
	}
	if v.Decision != DecisionError {
		t.Errorf("decision = %q, want ERROR", v.Decision)
	}
	if len(*calls) != 1 {
		t.Errorf("max_resumes=0 must not resume, got %d invocations", len(*calls))
	}
}

// A stream that never produces a result event has no report to read, which is
// an ERROR rather than a silent pass.
func TestClaudeReviewWithoutResultEventErrors(t *testing.T) {
	e := newClaude(config.ClaudeSettings{}, "nudge")
	fakeClaude(e, `{"type":"system","subtype":"init","session_id":"s1"}`+"\n")

	v, err := e.Review(context.Background(), Request{Prompt: "go", WorkDir: t.TempDir()})
	if err == nil || v.Decision != DecisionError {
		t.Errorf("verdict = %+v, err = %v; want an ERROR", v, err)
	}
	if !strings.Contains(err.Error(), "claude -p") {
		t.Errorf("error must name the engine, got %v", err)
	}
}

// The driver must carry cost out to the Verdict, including on a run that
// failed: the spend happened either way, and an ERROR that hides its cost is
// exactly the row you want when the budget looks wrong.
func TestClaudeReviewReportsCost(t *testing.T) {
	e := newClaude(config.ClaudeSettings{}, "nudge")
	e.runCmd = func(_ context.Context, _ []string, _ string, stream, _ io.Writer) error {
		_, _ = io.WriteString(stream, `{"type":"result","subtype":"success","structured_output":{"decision":"APPROVED","summary":"ok"},"usage":{"input_tokens":100},"total_cost_usd":0.4242}`+"\n")
		return nil
	}
	v, err := e.Review(context.Background(), Request{Prompt: "go", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if v.CostUSD != 0.4242 {
		t.Errorf("CostUSD = %v, want the engine-reported figure", v.CostUSD)
	}

	zero := 0
	failing := newClaude(config.ClaudeSettings{MaxResumes: &zero}, "nudge")
	failing.runCmd = func(_ context.Context, _ []string, _ string, stream, _ io.Writer) error {
		_, _ = io.WriteString(stream, `{"type":"result","subtype":"success","usage":{"input_tokens":100},"total_cost_usd":0.99}`+"\n")
		return nil
	}
	v, err = failing.Review(context.Background(), Request{Prompt: "go", WorkDir: t.TempDir()})
	if err == nil {
		t.Fatal("a run with no report must error")
	}
	if v.CostUSD != 0.99 {
		t.Errorf("a failed run must still report its spend, got %v", v.CostUSD)
	}
}

// codex supplies no cost accessor at all; the shared resolver must treat that
// as "unreported" rather than panicking on a nil func.
func TestResumableRunToleratesMissingCostAccessor(t *testing.T) {
	v, err := resumableRun{
		engine: "test",
		start:  func() error { return nil },
		report: func() (Verdict, error) { return Verdict{Decision: DecisionApproved}, nil },
		raw:    func() string { return "" },
		tokens: func() int { return 7 },
	}.do()
	if err != nil {
		t.Fatal(err)
	}
	if v.CostUSD != 0 || v.TokensUsed != 7 {
		t.Errorf("verdict = %+v", v)
	}
}

// The 21212 shape end to end: the driver must surface the CLI's reason in the
// error AND in the transcript the dashboard renders.
func TestClaudeFailureSurfacesReason(t *testing.T) {
	zero := 0
	e := newClaude(config.ClaudeSettings{MaxResumes: &zero}, "nudge")
	e.runCmd = func(_ context.Context, _ []string, _ string, stream, _ io.Writer) error {
		_, _ = io.WriteString(stream, `{"type":"system","subtype":"init","session_id":"s1"}`+"\n"+
			`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"credit balance too low","usage":{"input_tokens":0}}`+"\n")
		return nil
	}
	v, err := e.Review(context.Background(), Request{Prompt: "go", WorkDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "credit balance too low") {
		t.Errorf("error = %v, want the CLI's reason", err)
	}
	if v.Decision != DecisionError {
		t.Errorf("decision = %q, want ERROR", v.Decision)
	}
	if !strings.Contains(v.Raw, "error\nerror_during_execution: credit balance too low") {
		t.Errorf("transcript must carry the reason:\n%s", v.Raw)
	}
}
