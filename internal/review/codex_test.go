package review

import (
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
