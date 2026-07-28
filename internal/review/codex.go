package review

import (
	"context"
	"encoding/json"
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
// it only launches the agent and reads the report. The verdict contract, the
// agent log, and the resume policy are shared with every other engine; see
// driver.go.
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
	e := &codexEngine{bin: bin, model: c.Model, effort: c.Effort, sandbox: sandbox, args: c.Args,
		maxResumes: resolveMaxResumes(c.MaxResumes), resumePrompt: resumePrompt}
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
	return Provenance{Engine: e.Name(), Model: e.model, Effort: e.effort, EngineVersion: e.codexVersion(ctx)}
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

func (e *codexEngine) Review(ctx context.Context, req Request) (Verdict, error) {
	workDir, schemaPath, err := prepareWorkspace(req.WorkDir)
	if err != nil {
		return Verdict{Decision: DecisionError}, err
	}
	lastMsgPath := filepath.Join(workDir, "verdict.json")

	sink, buf, closeSink := newAgentSink(workDir)
	defer closeSink()

	// codex reports through a file (--output-last-message) and prints both its
	// session id and a per-invocation token trailer into the transcript, so
	// every accessor the resume policy needs is a read of one of those two.
	return resumableRun{
		engine:  "codex exec",
		max:     e.maxResumes,
		start:   func() error { return e.runCmd(ctx, e.buildArgs(workDir, schemaPath, lastMsgPath, req.Prompt), sink) },
		resume:  func(id string) error { return e.runCmd(ctx, e.buildResumeArgs(id, schemaPath, lastMsgPath), sink) },
		report:  func() (Verdict, error) { return parseVerdictFile(lastMsgPath) },
		session: func() string { return parseSessionID(buf.String()) },
		raw:     buf.String,
		// codex's trailer is a single number with no cache line, and it stays
		// in the same ~130k band across a 20-turn review that claude reports
		// millions for — a count that re-read context every turn could not do.
		// So it is a fresh count, recorded as one here rather than left for a
		// reader downstream to infer from its magnitude.
		usage: func() TokenUsage { return TokenUsage{Fresh: parseTokensUsed(buf.String())} },
	}.do()
}

// buildArgs assembles the codex exec invocation. Pure. The CLI contract
// (flag set, extra args, reporting instruction appended to the prompt) is
// pinned by table tests instead of live codex runs.
func (e *codexEngine) buildArgs(workDir, schemaPath, lastMsgPath, prompt string) []string {
	args := append([]string{"exec"}, e.modelArgs()...)
	args = append(args,
		"--sandbox", e.sandbox,
		"--cd", workDir,
		"--skip-git-repo-check", // the per-PR workdir is scratch space, not a repo
		"--output-schema", schemaPath,
		"--output-last-message", lastMsgPath,
	)
	args = append(args, e.args...)
	args = append(args, e.effortArgs()...)
	return append(args, prompt+reportingInstruction)
}

// buildResumeArgs assembles the codex exec resume invocation that nudges a
// session which ended on a WORKING report. resume has no --sandbox/--cd
// flags: the session's cwd is restored from its rollout, and the sandbox
// mode is re-asserted through its config key so the resumed turns keep the
// same write scope. Pure, pinned by table tests like buildArgs.
func (e *codexEngine) buildResumeArgs(sessionID, schemaPath, lastMsgPath string) []string {
	args := append([]string{"exec", "resume"}, e.modelArgs()...)
	// JSON string syntax is valid TOML basic-string syntax (see effortArgs).
	sandbox, _ := json.Marshal(e.sandbox)
	args = append(args,
		"--skip-git-repo-check",
		"-c", "sandbox_mode="+string(sandbox),
		"--output-schema", schemaPath,
		"--output-last-message", lastMsgPath,
	)
	args = append(args, e.args...)
	args = append(args, e.effortArgs()...)
	return append(args, sessionID, e.resumePrompt)
}

// modelArgs and effortArgs are the flags both invocations share. Kept as two
// helpers rather than one because they sit at different positions in the argv
// (model right after the subcommand, effort after the caller's extra args),
// which is why a single commonArgs like claude.go's does not fit here.
func (e *codexEngine) modelArgs() []string {
	if e.model == "" {
		return nil
	}
	return []string{"--model", e.model}
}

// effortArgs encodes the reasoning effort as a `-c` config override. JSON
// string syntax is valid TOML basic-string syntax, which keeps this safe even
// when a future effort name contains punctuation.
func (e *codexEngine) effortArgs() []string {
	if e.effort == "" {
		return nil
	}
	effort, _ := json.Marshal(e.effort)
	return []string{"-c", "model_reasoning_effort=" + string(effort)}
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

// parseVerdictFile reads and validates the agent's final-message report.
func parseVerdictFile(path string) (Verdict, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Verdict{}, err
	}
	return parseVerdict(data)
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
