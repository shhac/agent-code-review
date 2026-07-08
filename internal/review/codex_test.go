package review

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
)

func TestBuildArgs(t *testing.T) {
	full := newCodex(config.CodexSettings{Model: "some-model", Sandbox: "read-only", Args: []string{"-c", "k=v"}})
	args := full.buildArgs("/wd", "/wd/schema.json", "/wd/last.json", "PROMPT")

	joined := strings.Join(args, " ")
	for _, want := range []string{
		"exec",
		"--model some-model",
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
	bare := newCodex(config.CodexSettings{})
	joined = strings.Join(bare.buildArgs("/wd", "s", "l", "P"), " ")
	if strings.Contains(joined, "--model") {
		t.Error("--model must be omitted when unset")
	}
	if !strings.Contains(joined, "--sandbox workspace-write") {
		t.Error("default sandbox must be workspace-write")
	}
}

func TestCodexReviewProcessResultBranches(t *testing.T) {
	t.Run("valid report wins over non-zero exit", func(t *testing.T) {
		engine := newCodex(config.CodexSettings{Bin: fakeCodex(t, `printf '%s\n' "raw line"
printf '%s\n' "tokens used"
printf '%s\n' "1,234"
printf '{"decision":"COMMENTED","summary":"left comments"}' > "$last_msg"
exit 7
`)})
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
`)})
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
`)})
		v, err := engine.Review(context.Background(), Request{WorkDir: t.TempDir(), Prompt: "P"})
		if err == nil {
			t.Fatal("expected parse error")
		}
		if v.Decision != DecisionError || !strings.Contains(v.Raw, "done") {
			t.Errorf("verdict = %+v, want ERROR with raw output", v)
		}
	})
}

func fakeCodex(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/sh
last_msg=""
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
