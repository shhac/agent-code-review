package review

import (
	"context"
	"os/exec"
	"strings"

	"github.com/shhac/agent-code-review/internal/config"
)

// codexEngine invokes `codex exec` non-interactively with the assembled prompt.
// Codex runs the review (typically via the pr-issue-review skill), posts to
// GitHub, and performs any post-approve Slack step itself — this driver only
// launches it and reads back a verdict.
type codexEngine struct {
	bin     string
	model   string
	sandbox string
	args    []string
}

func newCodex(c config.CodexSettings) *codexEngine {
	bin := c.Bin
	if bin == "" {
		bin = "codex"
	}
	return &codexEngine{bin: bin, model: c.Model, sandbox: c.Sandbox, args: c.Args}
}

func (e *codexEngine) Name() string { return "codex" }

func (e *codexEngine) Review(ctx context.Context, req Request) (Verdict, error) {
	args := []string{"exec"}
	if e.model != "" {
		args = append(args, "--model", e.model)
	}
	if e.sandbox != "" {
		args = append(args, "--sandbox", e.sandbox)
	}
	if req.WorkDir != "" {
		args = append(args, "--cd", req.WorkDir)
	}
	args = append(args, e.args...)
	args = append(args, req.Prompt)

	cmd := exec.CommandContext(ctx, e.bin, args...)
	out, err := cmd.CombinedOutput()
	raw := string(out)
	if err != nil {
		return Verdict{Decision: DecisionError, Raw: raw}, err
	}
	return Verdict{Decision: parseDecision(raw), Raw: raw}, nil
}

// parseDecision reads the engine's outcome from its transcript. The engine
// signals APPROVE via the pr-issue-review review markers; absent a clear
// approve signal we record COMMENT (the safe default).
//
// TODO: tighten this once the engine emits a structured result — ideally the
// verdict is read back from the posted GitHub review rather than the transcript.
func parseDecision(out string) string {
	upper := strings.ToUpper(out)
	if strings.Contains(upper, "APPROVE") && !strings.Contains(upper, "DO NOT APPROVE") {
		return DecisionApprove
	}
	return DecisionComment
}
