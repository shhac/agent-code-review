package review

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/shhac/agent-code-review/internal/config"
)

// codexEngine invokes `codex exec` non-interactively with the assembled prompt.
// The agent performs the review itself — posting the approve/comment to GitHub
// and running any post-approve steps — and then REPORTS BACK what it did as a
// schema-constrained final message (--output-schema + --output-last-message),
// which this driver parses into a Verdict. The engine never posts the review;
// it only launches the agent and reads the report.
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
	sandbox := c.Sandbox
	if sandbox == "" {
		// The agent needs to write scratch files and run gh; workspace-write
		// scopes that to the per-PR workdir.
		sandbox = "workspace-write"
	}
	return &codexEngine{bin: bin, model: c.Model, sandbox: sandbox, args: c.Args}
}

func (e *codexEngine) Name() string { return "codex" }

// verdictSchema constrains the agent's final message. ERROR is deliberately
// absent — it is this driver's own value for "the invocation failed", never
// something the agent reports.
const verdictSchema = `{
  "type": "object",
  "properties": {
    "decision": {
      "type": "string",
      "enum": ["APPROVED", "COMMENTED", "SKIPPED"],
      "description": "What you actually did: APPROVED = submitted an approving review; COMMENTED = left a review or comments without approving; SKIPPED = did not review this PR."
    },
    "summary": {
      "type": "string",
      "description": "One or two sentences on what you did and why."
    }
  },
  "required": ["decision", "summary"],
  "additionalProperties": false
}`

// reportingInstruction is appended to every prompt so the agent knows its final
// message is a machine-read report, not prose.
const reportingInstruction = `

When you are completely finished, your FINAL message must be a JSON object matching the provided output schema: {"decision": "APPROVED"|"COMMENTED"|"SKIPPED", "summary": "..."}. The decision must reflect what you ACTUALLY did on GitHub — APPROVED only if you submitted an approving review, COMMENTED if you left a review or comments without approving, SKIPPED if you did not review this PR (explain why in the summary).`

func (e *codexEngine) Review(ctx context.Context, req Request) (Verdict, error) {
	workDir := req.WorkDir
	if workDir == "" {
		dir, err := os.MkdirTemp("", "agent-code-review-")
		if err != nil {
			return Verdict{Decision: DecisionError}, err
		}
		workDir = dir
	}

	schemaPath := filepath.Join(workDir, "verdict.schema.json")
	if err := os.WriteFile(schemaPath, []byte(verdictSchema), 0o600); err != nil {
		return Verdict{Decision: DecisionError}, err
	}
	lastMsgPath := filepath.Join(workDir, "verdict.json")

	args := []string{"exec"}
	if e.model != "" {
		args = append(args, "--model", e.model)
	}
	args = append(args,
		"--sandbox", e.sandbox,
		"--cd", workDir,
		"--skip-git-repo-check", // the per-PR workdir is scratch space, not a repo
		"--output-schema", schemaPath,
		"--output-last-message", lastMsgPath,
	)
	args = append(args, e.args...)
	args = append(args, req.Prompt+reportingInstruction)

	cmd := exec.CommandContext(ctx, e.bin, args...)
	out, runErr := cmd.CombinedOutput()
	raw := string(out)

	// Prefer the report file even when the process exited non-zero — a partial
	// run may still have written a valid final message.
	verdict, parseErr := parseVerdictFile(lastMsgPath)
	if parseErr == nil {
		verdict.Raw = raw
		return verdict, nil
	}
	if runErr != nil {
		return Verdict{Decision: DecisionError, Raw: raw}, fmt.Errorf("codex exec: %w", runErr)
	}
	return Verdict{Decision: DecisionError, Raw: raw}, fmt.Errorf("codex exec succeeded but no verdict report: %w", parseErr)
}

// parseVerdictFile reads and validates the agent's final-message report.
func parseVerdictFile(path string) (Verdict, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Verdict{}, err
	}
	return parseVerdict(data)
}

func parseVerdict(data []byte) (Verdict, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return Verdict{}, fmt.Errorf("empty verdict report")
	}
	var v Verdict
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return Verdict{}, fmt.Errorf("parse verdict report: %w", err)
	}
	switch v.Decision {
	case DecisionApproved, DecisionCommented, DecisionSkipped:
		return v, nil
	default:
		return Verdict{}, fmt.Errorf("verdict report has invalid decision %q", v.Decision)
	}
}
