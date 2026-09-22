package review

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
)

// invocation is one engine subprocess as the harness asked for it: the argv
// and the directory the process would have started in.
type invocation struct {
	args []string
	dir  string
}

// sent runs one review through e with the harness's process seam replaced,
// and returns every invocation it made. Each one finishes the review with an
// APPROVED report the way its engine delivers one, so a test asserts exactly
// what Review sends rather than a request rebuilt beside it.
func sent(t *testing.T, e *nativeEngine, req Request) []invocation {
	t.Helper()
	var calls []invocation
	e.cfg.RunCommand = func(_ context.Context, args []string, dir string, stdout, _ io.Writer) error {
		calls = append(calls, invocation{args: args, dir: dir})
		return approve(t, e.cfg.Engine, args, stdout)
	}
	if req.WorkDir == "" {
		req.WorkDir = t.TempDir()
	}
	if _, err := e.Review(context.Background(), req); err != nil {
		t.Fatalf("review: %v", err)
	}
	return calls
}

// approve answers one invocation with an APPROVED report: claude's arrives in
// the result event, codex's in the file named by --output-last-message.
func approve(t *testing.T, engine string, args []string, stdout io.Writer) error {
	t.Helper()
	if engine == "claude" {
		_, err := io.WriteString(stdout, resultLine(t, "s1", DecisionApproved, 1))
		return err
	}
	_, _ = io.WriteString(stdout, `{"type":"turn.completed","usage":{"input_tokens":1}}`+"\n")
	path, ok := argValue(args, "--output-last-message")
	if !ok {
		t.Fatalf("codex invocation names no report file: %v", args)
	}
	return os.WriteFile(path, []byte(`{"decision":"APPROVED","summary":"ok"}`), 0o600)
}

// Both engines run IN the review's workspace, fresh or resumed. claude has no
// directory flag, so its process directory is the only way to say where to
// work. codex has --cd, but `codex exec resume` does not accept it, so a
// resumed codex took whatever directory the daemon happened to be started in,
// and its workspace-write sandbox was scoped there rather than to the PR's
// workspace.
func TestEachEngineRunsInItsWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name   string
		engine *nativeEngine
	}{
		{"codex", newCodex(config.CodexSettings{}, "nudge")},
		{"claude", newClaude(config.ClaudeSettings{}, "nudge")},
	} {
		for _, session := range []string{"", "prev-session"} {
			t.Run(tc.name+"/resume="+session, func(t *testing.T) {
				workDir := t.TempDir()
				calls := sent(t, tc.engine, Request{Prompt: "p", WorkDir: workDir, ResumeSession: session})
				if len(calls) != 1 {
					t.Fatalf("invocations = %d, want 1", len(calls))
				}
				if got := calls[0].dir; got != workDir {
					t.Errorf("process dir = %q, want the workspace %q", got, workDir)
				}
			})
		}
	}
}

// Provenance reports the dials the engine was BUILT with, defaults applied,
// because that is what ran: the claude run reports no effort back, so an
// unresolved figure here would be an unrecorded one in history.
func TestProvenanceReportsTheResolvedDials(t *testing.T) {
	missing := t.TempDir() + "/no-such-cli"
	for _, tc := range []struct {
		name                  string
		engine                *nativeEngine
		wantModel, wantEffort string
	}{
		{"codex unset", newCodex(config.CodexSettings{}, "n"), "", ""},
		{"codex pinned", newCodex(config.CodexSettings{EngineCommon: config.EngineCommon{Model: "m", Effort: "high"}}, "n"), "m", "high"},
		{"claude unset", newClaude(config.ClaudeSettings{}, "n"), defaultModel, defaultEffort},
		{"claude pinned", newClaude(config.ClaudeSettings{EngineCommon: config.EngineCommon{Model: "sonnet", Effort: "xhigh"}}, "n"), "sonnet", "xhigh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.engine.cfg.Binary = missing
			p := tc.engine.Provenance(context.Background())
			if p.Engine != tc.engine.cfg.Engine || p.Model != tc.wantModel || p.Effort != tc.wantEffort {
				t.Errorf("provenance = %+v, want %s/%q/%q", p, tc.engine.cfg.Engine, tc.wantModel, tc.wantEffort)
			}
			if p.EngineVersion != "" {
				t.Errorf("a failed version probe must record no version, got %q", p.EngineVersion)
			}
		})
	}
}

// An unwired engine has no dials: it must not borrow the default engine's, the
// way the raw config's fallback to codex's block would.
func TestResolvedDialsOfAnUnwiredEngineAreEmpty(t *testing.T) {
	cfg := config.ReviewSettings{Engine: "gemini", Codex: config.CodexSettings{EngineCommon: config.EngineCommon{Model: "m", Effort: "high"}}}
	if model, effort := ResolvedDials(cfg); model != "" || effort != "" {
		t.Errorf("dials = %q/%q, want none", model, effort)
	}
}
