package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
// The agent performs the review itself (posting the approve/comment to GitHub
// and running any post-approve steps) and then REPORTS BACK what it did as a
// schema-constrained final message (--output-schema + --output-last-message),
// which this driver parses into a Verdict. The engine never posts the review;
// it only launches the agent and reads the report.
type codexEngine struct {
	bin          string
	model        string
	effort       string
	sandbox      string
	args         []string
	maxResumes   int
	resumePrompt string

	// runCmd launches one codex invocation with its output teed into sink:
	// the engine's only subprocess seam. Production execs e.bin; tests inject
	// a recorder so the resume loop and outcome precedence test in-process.
	runCmd func(ctx context.Context, args []string, sink io.Writer) error
}

// defaultMaxResumes bounds the resume-on-WORKING nudges per review when
// codex.max_resumes is unset.
const defaultMaxResumes = 2

func newCodex(c config.CodexSettings, resumePrompt string) *codexEngine {
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
	resumes := defaultMaxResumes
	if c.MaxResumes != nil && *c.MaxResumes >= 0 {
		resumes = *c.MaxResumes
	}
	e := &codexEngine{bin: bin, model: c.Model, effort: c.Effort, sandbox: sandbox, args: c.Args,
		maxResumes: resumes, resumePrompt: resumePrompt}
	e.runCmd = e.execCodex
	return e
}

// execCodex is the production runCmd: one codex subprocess, stdout+stderr
// teed into sink.
func (e *codexEngine) execCodex(ctx context.Context, args []string, sink io.Writer) error {
	cmd := exec.CommandContext(ctx, e.bin, args...)
	cmd.Stdout = sink
	cmd.Stderr = sink
	return cmd.Run()
}

func (e *codexEngine) Name() string { return "codex" }

func (e *codexEngine) Provenance(ctx context.Context) Provenance {
	return Provenance{Engine: e.Name(), Model: e.model, Effort: e.effort, CodexVersion: e.codexVersion(ctx)}
}

// codexVersion probes `codex --version` uncached: the engine is rebuilt from
// live config at the start of every cycle and reviews take minutes, so one
// cheap exec per Provenance call needs no cache (and recording the version
// at review end stays accurate across a mid-cycle codex upgrade). "" on a
// failed probe.
func (e *codexEngine) codexVersion(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, e.bin, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// verdictSchema constrains the agent's messages. codex applies the schema to
// EVERY assistant message in the run, not just the final report, so WORKING
// exists as the honest value for intermediate progress notes; without it
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

Every message you emit matches the provided output schema. While you are still working, use {"decision": "WORKING", "summary": "<progress note>"} for intermediate updates. When you are completely finished, your FINAL message must report the outcome: {"decision": "APPROVED"|"COMMENTED"|"REQUESTED_CHANGES"|"SKIPPED", "summary": "..."}. The final decision must reflect what you ACTUALLY did on GitHub: APPROVED only if you submitted an approving review, COMMENTED if you left a review or comments without approving, REQUESTED_CHANGES if you submitted a request-changes review, SKIPPED if you did not review this PR (explain why in the summary). Never end on WORKING.`

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

	sink, buf, closeSink := newAgentSink(workDir)
	defer closeSink()
	verdict, parseErr, runErr := e.runWithResumes(ctx, req.Prompt, workDir, schemaPath, lastMsgPath, sink, buf)
	return resolveOutcome(verdict, parseErr, runErr, buf.String())
}

// runWithResumes drives the initial exec and, when a clean exit's last
// message is WORKING (the agent yielded its turn without a tool call and
// codex took that as the final answer — the session is intact and nothing
// was posted), resumes the session with a nudge, up to maxResumes times,
// instead of burning the whole run as an ERROR.
func (e *codexEngine) runWithResumes(ctx context.Context, prompt, workDir, schemaPath, lastMsgPath string, sink io.Writer, buf *bytes.Buffer) (Verdict, error, error) {
	runErr := e.runCmd(ctx, e.buildArgs(workDir, schemaPath, lastMsgPath, prompt), sink)
	verdict, parseErr := parseVerdictFile(lastMsgPath)
	for resumed := 0; resumed < e.maxResumes && runErr == nil && errors.Is(parseErr, errEndedOnWorking); resumed++ {
		sessionID := parseSessionID(buf.String())
		if sessionID == "" {
			break
		}
		runErr = e.runCmd(ctx, e.buildResumeArgs(sessionID, schemaPath, lastMsgPath), sink)
		verdict, parseErr = parseVerdictFile(lastMsgPath)
	}
	return verdict, parseErr, runErr
}

// resolveOutcome applies the driver's precedence rules to one finished run:
// a valid report wins even over a non-zero exit (a partial run may still
// have written its final message); otherwise the process failure, and last
// a clean exit that never produced a report.
func resolveOutcome(verdict Verdict, parseErr, runErr error, raw string) (Verdict, error) {
	tokens := parseTokensUsed(raw)
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
// that can't hold the log file degrades to buffer-only; diagnostics must
// survive even when the live view can't.
func newAgentSink(workDir string) (io.Writer, *bytes.Buffer, func()) {
	buf := &bytes.Buffer{}
	logFile, err := os.Create(LogPath(workDir))
	if err != nil {
		return buf, buf, func() {}
	}
	return io.MultiWriter(buf, logFile), buf, func() { _ = logFile.Close() }
}

// buildArgs assembles the codex exec invocation. Pure. The CLI contract
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

// buildResumeArgs assembles the codex exec resume invocation that nudges a
// session which ended on a WORKING report. resume has no --sandbox/--cd
// flags: the session's cwd is restored from its rollout, and the sandbox
// mode is re-asserted through its config key so the resumed turns keep the
// same write scope. Pure, pinned by table tests like buildArgs.
func (e *codexEngine) buildResumeArgs(sessionID, schemaPath, lastMsgPath string) []string {
	args := []string{"exec", "resume"}
	if e.model != "" {
		args = append(args, "--model", e.model)
	}
	// JSON string syntax is valid TOML basic-string syntax (see the effort
	// override below).
	sandbox, _ := json.Marshal(e.sandbox)
	args = append(args,
		"--skip-git-repo-check",
		"-c", "sandbox_mode="+string(sandbox),
		"--output-schema", schemaPath,
		"--output-last-message", lastMsgPath,
	)
	args = append(args, e.args...)
	if e.effort != "" {
		effort, _ := json.Marshal(e.effort)
		args = append(args, "-c", "model_reasoning_effort="+string(effort))
	}
	return append(args, sessionID, e.resumePrompt)
}

// sessionIDPattern matches the "session id:" line of codex exec's run header.
var sessionIDPattern = regexp.MustCompile(`(?m)^session id: ([0-9a-fA-F-]{36})\s*$`)

// parseSessionID extracts the run's session UUID from the engine transcript;
// "" means the header wasn't found (and a resume is impossible).
func parseSessionID(raw string) string {
	m := sessionIDPattern.FindStringSubmatch(raw)
	if m == nil {
		return ""
	}
	return m[1]
}

// errEndedOnWorking marks a run whose final message was an intermediate
// WORKING report: the agent yielded early, and the driver may resume it.
var errEndedOnWorking = errors.New("agent ended on an intermediate WORKING report (run truncated?)")

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
		return Verdict{}, errEndedOnWorking
	default:
		return Verdict{}, fmt.Errorf("verdict report has invalid decision %q", v.Decision)
	}
}

// tokensUsedPattern matches the "tokens used" trailer codex exec prints at
// the end of a run, e.g. "tokens used\n192,575".
var tokensUsedPattern = regexp.MustCompile(`(?m)^tokens used\n([0-9,]+)$`)

// parseTokensUsed sums the run's token count from the engine transcript. Each
// codex invocation prints its own per-invocation trailer, so with resumes the
// transcript holds several and the total spend is their sum (verified live:
// a resumed invocation reports only its own usage, not the session's).
// 0 means no trailer was found (truncated or older codex).
func parseTokensUsed(raw string) int {
	total := 0
	for _, m := range tokensUsedPattern.FindAllStringSubmatch(raw, -1) {
		n, err := strconv.Atoi(strings.ReplaceAll(m[1], ",", ""))
		if err != nil {
			continue
		}
		total += n
	}
	return total
}
