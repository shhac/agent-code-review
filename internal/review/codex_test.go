package review

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
)

func TestBuildArgs(t *testing.T) {
	full := newCodex(config.CodexSettings{Model: "some-model", Effort: "high", Sandbox: "read-only", Args: []string{"-c", "k=v"}}, "NUDGE")
	args := full.buildArgs("/wd", "/wd/schema.json", "/wd/last.json", "PROMPT")

	joined := strings.Join(args, " ")
	for _, want := range []string{
		"exec",
		"--model some-model",
		`-c model_reasoning_effort="high"`,
		"--sandbox read-only",
		"--cd /wd",
		"--skip-git-repo-check",
		"--output-schema /wd/schema.json",
		"--output-last-message /wd/last.json",
		"-c k=v",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %v", want, args)
		}
	}
	// The prompt is the final arg and carries the reporting instruction:
	// WORKING for intermediate progress, a real outcome as the final message.
	last := args[len(args)-1]
	if !strings.HasPrefix(last, "PROMPT") || !strings.Contains(last, `"WORKING"`) || !strings.Contains(last, "FINAL message must report the outcome") {
		t.Errorf("prompt arg malformed: %.120s", last)
	}

	// No model configured → no --model flag; defaults still applied.
	bare := newCodex(config.CodexSettings{}, "NUDGE")
	joined = strings.Join(bare.buildArgs("/wd", "s", "l", "P"), " ")
	if strings.Contains(joined, "--model") {
		t.Error("--model must be omitted when unset")
	}
	if strings.Contains(joined, "model_reasoning_effort") {
		t.Error("model_reasoning_effort must be omitted when unset")
	}
	if !strings.Contains(joined, "--sandbox workspace-write") {
		t.Error("default sandbox must be workspace-write")
	}
}

func TestBuildResumeArgs(t *testing.T) {
	e := newCodex(config.CodexSettings{Model: "some-model", Effort: "high", Sandbox: "read-only", Args: []string{"-c", "k=v"}}, "NUDGE")
	args := e.buildResumeArgs("SESSION-ID", "/wd/schema.json", "/wd/last.json")

	joined := strings.Join(args, " ")
	for _, want := range []string{
		"exec resume",
		"--model some-model",
		"--skip-git-repo-check",
		`-c sandbox_mode="read-only"`, // resume has no --sandbox flag; the mode travels as a config override
		"--output-schema /wd/schema.json",
		"--output-last-message /wd/last.json",
		"-c k=v",
		`-c model_reasoning_effort="high"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("resume args missing %q: %v", want, args)
		}
	}
	// exec-only flags must not leak into resume, which rejects them.
	for _, banned := range []string{"--sandbox", "--cd"} {
		if slices.Contains(args, banned) {
			t.Errorf("resume args must not carry %s: %v", banned, args)
		}
	}
	// The session and the nudge prompt are the positional tail.
	if n := len(args); args[n-2] != "SESSION-ID" || args[n-1] != "NUDGE" {
		t.Errorf("resume args must end with session id + nudge, got %v", args[len(args)-2:])
	}
}

func TestCodexReviewProcessResultBranches(t *testing.T) {
	t.Run("valid report wins over non-zero exit", func(t *testing.T) {
		engine := newCodex(config.CodexSettings{Bin: fakeCodex(t, `printf '%s\n' "raw line"
printf '%s\n' "tokens used"
printf '%s\n' "1,234"
printf '{"decision":"COMMENTED","summary":"left comments"}' > "$last_msg"
exit 7
`)}, "NUDGE")
		v, err := engine.Review(context.Background(), Request{WorkDir: t.TempDir(), Prompt: "P"})
		if err != nil {
			t.Fatal(err)
		}
		if v.Decision != DecisionCommented || v.Summary != "left comments" || v.TokensUsed != 1234 || !strings.Contains(v.Raw, "raw line") {
			t.Errorf("verdict = %+v, want COMMENTED with raw output and tokens", v)
		}
	})

	t.Run("non-zero without report returns error verdict", func(t *testing.T) {
		engine := newCodex(config.CodexSettings{Bin: fakeCodex(t, `printf '%s\n' diagnostics
printf '%s\n' "tokens used"
printf '%s\n' "941"
exit 7
`)}, "NUDGE")
		v, err := engine.Review(context.Background(), Request{WorkDir: t.TempDir(), Prompt: "P"})
		if err == nil {
			t.Fatal("expected codex exec error")
		}
		if v.Decision != DecisionError || v.TokensUsed != 941 || !strings.Contains(v.Raw, "diagnostics") {
			t.Errorf("verdict = %+v, want ERROR with raw output and tokens", v)
		}
	})

	t.Run("zero exit with invalid report returns error verdict", func(t *testing.T) {
		engine := newCodex(config.CodexSettings{Bin: fakeCodex(t, `printf '%s\n' done
printf 'not json' > "$last_msg"
exit 0
`)}, "NUDGE")
		v, err := engine.Review(context.Background(), Request{WorkDir: t.TempDir(), Prompt: "P"})
		if err == nil {
			t.Fatal("expected parse error")
		}
		if v.Decision != DecisionError || !strings.Contains(v.Raw, "done") {
			t.Errorf("verdict = %+v, want ERROR with raw output", v)
		}
	})
}

// workingThenBody simulates the observed failure mode: the initial exec ends
// cleanly on a WORKING report (after printing its session header), and each
// resume invocation runs resumeBody instead. Every invocation appends a line
// to $workdir/invocations and prints a 100-token trailer.
func workingThenBody(resumeBody string) string {
	return `echo "$(echo "$all_args" | tr '\n' ' ')" >> "$(dirname "$last_msg")/invocations"
if [ "$resume" = 1 ]; then
` + resumeBody + `
else
  printf 'session id: 019f6f77-3c3d-7ce3-966d-d4b2083f4459\n'
  printf '{"decision":"WORKING","summary":"starting"}' > "$last_msg"
fi
printf '%s\n' "tokens used"
printf '%s\n' "100"
exit 0
`
}

func invocations(t *testing.T, workDir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(workDir, "invocations"))
	if err != nil {
		t.Fatalf("fake codex recorded no invocations: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestCodexResumeOnWorking(t *testing.T) {
	t.Run("resumes the session and sums the token trailers", func(t *testing.T) {
		engine := newCodex(config.CodexSettings{Bin: fakeCodex(t,
			workingThenBody(`  printf '{"decision":"APPROVED","summary":"finished after nudge"}' > "$last_msg"`),
		)}, "keep going until you arrive at a decision")
		workDir := t.TempDir()
		v, err := engine.Review(context.Background(), Request{WorkDir: workDir, Prompt: "P"})
		if err != nil {
			t.Fatal(err)
		}
		if v.Decision != DecisionApproved || v.Summary != "finished after nudge" {
			t.Errorf("verdict = %+v, want the resumed APPROVED report", v)
		}
		if v.TokensUsed != 200 {
			t.Errorf("tokens = %d, want both invocations' trailers summed (200)", v.TokensUsed)
		}
		calls := invocations(t, workDir)
		if len(calls) != 2 {
			t.Fatalf("invocations = %d, want exec + one resume: %q", len(calls), calls)
		}
		// The resume must target the parsed session and carry the nudge.
		if !strings.Contains(calls[1], "exec resume") ||
			!strings.Contains(calls[1], "019f6f77-3c3d-7ce3-966d-d4b2083f4459") ||
			!strings.Contains(calls[1], "keep going until you arrive at a decision") {
			t.Errorf("resume invocation malformed: %q", calls[1])
		}
	})

	t.Run("gives up after max_resumes and records ERROR", func(t *testing.T) {
		engine := newCodex(config.CodexSettings{Bin: fakeCodex(t,
			workingThenBody(`  printf '{"decision":"WORKING","summary":"still going"}' > "$last_msg"`),
		)}, "NUDGE")
		workDir := t.TempDir()
		v, err := engine.Review(context.Background(), Request{WorkDir: workDir, Prompt: "P"})
		if err == nil || v.Decision != DecisionError {
			t.Fatalf("verdict = %+v err=%v, want ERROR after exhausting resumes", v, err)
		}
		if got := len(invocations(t, workDir)); got != 1+defaultMaxResumes {
			t.Errorf("invocations = %d, want the exec plus %d resumes", got, defaultMaxResumes)
		}
	})

	t.Run("max_resumes 0 disables resuming", func(t *testing.T) {
		zero := 0
		engine := newCodex(config.CodexSettings{MaxResumes: &zero, Bin: fakeCodex(t,
			workingThenBody(`  printf '{"decision":"APPROVED","summary":"never reached"}' > "$last_msg"`),
		)}, "NUDGE")
		workDir := t.TempDir()
		v, err := engine.Review(context.Background(), Request{WorkDir: workDir, Prompt: "P"})
		if err == nil || v.Decision != DecisionError {
			t.Fatalf("verdict = %+v err=%v, want ERROR without resuming", v, err)
		}
		if got := len(invocations(t, workDir)); got != 1 {
			t.Errorf("invocations = %d, want just the initial exec", got)
		}
	})

	t.Run("no session header means no resume", func(t *testing.T) {
		engine := newCodex(config.CodexSettings{Bin: fakeCodex(t,
			`echo "$(echo "$all_args" | tr '\n' ' ')" >> "$(dirname "$last_msg")/invocations"
printf '{"decision":"WORKING","summary":"starting"}' > "$last_msg"
exit 0
`)}, "NUDGE")
		workDir := t.TempDir()
		v, err := engine.Review(context.Background(), Request{WorkDir: workDir, Prompt: "P"})
		if err == nil || v.Decision != DecisionError {
			t.Fatalf("verdict = %+v err=%v, want ERROR when the session id is unknown", v, err)
		}
		if got := len(invocations(t, workDir)); got != 1 {
			t.Errorf("invocations = %d, want just the initial exec", got)
		}
	})
}

// fakeCodex writes a stand-in codex binary running body with $last_msg (the
// --output-last-message path), $resume (1 on an `exec resume` invocation),
// and $all_args (the full argv) in scope.
func fakeCodex(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/sh
last_msg=""
resume=0
[ "$2" = "resume" ] && resume=1
all_args="$*"
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output-last-message" ]; then
    shift
    last_msg="$1"
  fi
  shift
done
` + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestNewAgentSink pins the tee contract: engine output lands in both the
// buffer (Verdict.Raw) and the workdir's live agent log, and a workspace
// that can't hold the log degrades to buffer-only instead of blanking out
// error diagnostics.
func TestNewAgentSink(t *testing.T) {
	t.Run("tees into the live log", func(t *testing.T) {
		dir := t.TempDir()
		sink, buf, closeSink := newAgentSink(dir)
		if _, err := io.WriteString(sink, "agent output\n"); err != nil {
			t.Fatal(err)
		}
		closeSink()
		if got := buf.String(); got != "agent output\n" {
			t.Errorf("buffer = %q, want the written output", got)
		}
		logged, err := os.ReadFile(LogPath(dir))
		if err != nil {
			t.Fatalf("live log must exist: %v", err)
		}
		if string(logged) != buf.String() {
			t.Errorf("log = %q, buffer = %q; the tee must keep them identical", logged, buf.String())
		}
	})

	t.Run("unwritable workdir keeps the buffer", func(t *testing.T) {
		sink, buf, closeSink := newAgentSink(filepath.Join(t.TempDir(), "missing", "nested"))
		defer closeSink()
		if _, err := io.WriteString(sink, "diagnostics"); err != nil {
			t.Fatal(err)
		}
		if buf.String() != "diagnostics" {
			t.Errorf("buffer = %q; Raw must survive a failed log create", buf.String())
		}
	})
}
