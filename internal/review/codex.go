package review

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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
	effort  string
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
	return &codexEngine{bin: bin, model: c.Model, effort: c.Effort, sandbox: sandbox, args: c.Args}
}

func (e *codexEngine) Name() string { return "codex" }

func (e *codexEngine) Provenance(ctx context.Context) Provenance {
	out, err := exec.CommandContext(ctx, e.bin, "--version").Output()
	version := ""
	if err == nil {
		version = strings.TrimSpace(string(out))
	}
	return Provenance{Engine: e.Name(), Model: e.model, Effort: e.effort, CodexVersion: version}
}

// verdictSchema constrains the agent's messages. codex applies the schema to
// EVERY assistant message in the run, not just the final report, so WORKING
// exists as the honest value for intermediate progress notes — without it
// the agent overloads SKIPPED for "I'm still investigating". ERROR is
// deliberately absent: it is this driver's own value for "the invocation
// failed", never something the agent reports.
const verdictSchema = `{
  "type": "object",
  "properties": {
    "decision": {
      "type": "string",
      "enum": ["WORKING", "APPROVED", "COMMENTED", "REQUESTED_CHANGES", "SKIPPED"],
      "description": "WORKING = you are not finished yet (use for every intermediate progress note; NEVER as your final message). The rest report what you actually did: APPROVED = submitted an approving review; COMMENTED = left a review or comments without approving; REQUESTED_CHANGES = submitted a request-changes review; SKIPPED = did not review this PR."
    },
    "summary": {
      "type": "string",
      "description": "One or two sentences: your progress note (WORKING), or what you did and why (final message)."
    }
  },
  "required": ["decision", "summary"],
  "additionalProperties": false
}`

// agentLogName is the live log file the engine tees its output into inside
// the review workdir; consumers locate it through LogPath.
const agentLogName = "agent.log"

// LogPath locates the review agent's live log inside its workspace. The
// engine tees its output there as the run progresses; the CLI's `queue log`
// and the dashboard's per-review page both tail it through this one contract.
func LogPath(workDir string) string {
	return filepath.Join(workDir, agentLogName)
}

// reportingInstruction is appended to every prompt so the agent knows its final
// message is a machine-read report, not prose.
const reportingInstruction = `

Every message you emit matches the provided output schema. While you are still working, use {"decision": "WORKING", "summary": "<progress note>"} for intermediate updates. When you are completely finished, your FINAL message must report the outcome: {"decision": "APPROVED"|"COMMENTED"|"REQUESTED_CHANGES"|"SKIPPED", "summary": "..."}. The final decision must reflect what you ACTUALLY did on GitHub — APPROVED only if you submitted an approving review, COMMENTED if you left a review or comments without approving, REQUESTED_CHANGES if you submitted a request-changes review, SKIPPED if you did not review this PR (explain why in the summary). Never end on WORKING.`

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

	args := e.buildArgs(workDir, schemaPath, lastMsgPath, req.Prompt)
	cmd := exec.CommandContext(ctx, e.bin, args...)

	sink, buf, closeSink := newAgentSink(workDir)
	defer closeSink()
	cmd.Stdout = sink
	cmd.Stderr = sink
	runErr := cmd.Run()
	raw := buf.String()

	// Prefer the report file even when the process exited non-zero — a partial
	// run may still have written a valid final message.
	tokens := parseTokensUsed(raw)
	verdict, parseErr := parseVerdictFile(lastMsgPath)
	if parseErr == nil {
		verdict.Raw = raw
		verdict.TokensUsed = tokens
		return verdict, nil
	}
	if runErr != nil {
		return Verdict{Decision: DecisionError, Raw: raw, TokensUsed: tokens}, fmt.Errorf("codex exec: %w", runErr)
	}
	return Verdict{Decision: DecisionError, Raw: raw, TokensUsed: tokens}, fmt.Errorf("codex exec succeeded but no verdict report: %w", parseErr)
}

// newAgentSink builds the writer engine output streams into: an in-memory
// buffer (it feeds Verdict.Raw for error surfacing) teed into the workdir's
// live agent log (see LogPath) as the run progresses, so the CLI's
// `queue log` and the dashboard's per-review page can watch it. A workspace
// that can't hold the log file degrades to buffer-only — diagnostics must
// survive even when the live view can't.
func newAgentSink(workDir string) (io.Writer, *bytes.Buffer, func()) {
	buf := &bytes.Buffer{}
	logFile, err := os.Create(LogPath(workDir))
	if err != nil {
		return buf, buf, func() {}
	}
	return io.MultiWriter(buf, logFile), buf, func() { _ = logFile.Close() }
}

// buildArgs assembles the codex exec invocation. Pure — the CLI contract
// (flag set, extra args, reporting instruction appended to the prompt) is
// pinned by table tests instead of live codex runs.
func (e *codexEngine) buildArgs(workDir, schemaPath, lastMsgPath, prompt string) []string {
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
	if e.effort != "" {
		// JSON string syntax is valid TOML basic-string syntax, which keeps this
		// config override safe even when a future effort name contains punctuation.
		effort, _ := json.Marshal(e.effort)
		args = append(args, "-c", "model_reasoning_effort="+string(effort))
	}
	return append(args, prompt+reportingInstruction)
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
	case DecisionApproved, DecisionCommented, DecisionRequestedChanges, DecisionSkipped:
		return v, nil
	case DecisionWorking:
		// WORKING is only legal mid-run; ending on it means the run was cut
		// short before a real outcome was reported.
		return Verdict{}, fmt.Errorf("agent ended on an intermediate WORKING report (run truncated?)")
	default:
		return Verdict{}, fmt.Errorf("verdict report has invalid decision %q", v.Decision)
	}
}

// tokensUsedPattern matches the "tokens used" trailer codex exec prints at
// the end of a run, e.g. "tokens used\n192,575".
var tokensUsedPattern = regexp.MustCompile(`(?m)^tokens used\n([0-9,]+)$`)

// parseTokensUsed extracts the run's token count from the engine transcript;
// 0 means the trailer wasn't found (truncated or older codex).
func parseTokensUsed(raw string) int {
	matches := tokensUsedPattern.FindAllStringSubmatch(raw, -1)
	if len(matches) == 0 {
		return 0
	}
	n, err := strconv.Atoi(strings.ReplaceAll(matches[len(matches)-1][1], ",", ""))
	if err != nil {
		return 0
	}
	return n
}
