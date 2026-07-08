package review

import (
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
	// The prompt is the final arg and carries the reporting instruction.
	last := args[len(args)-1]
	if !strings.HasPrefix(last, "PROMPT") || !strings.Contains(last, "FINAL message must be a JSON object") {
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
