package review

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	stream := newCodexTranscoder(sink)

	// codex reports its verdict through a file (--output-last-message); the
	// session id and the token split come off the --json event stream, which
	// the transcoder renders into the shared marker format as it goes.
	return resumableRun{
		engine: "codex exec",
		max:    e.maxResumes,
		start: func() error {
			// An interrupted attempt left a live session; continuing it costs
			// the nudge instead of the whole review again.
			if req.ResumeSession != "" {
				stream.userPrompt(e.resumePrompt)
				return e.run(ctx, e.buildResumeArgs(req.ResumeSession, schemaPath, lastMsgPath), stream)
			}
			stream.userPrompt(req.Prompt)
			return e.run(ctx, e.buildArgs(workDir, schemaPath, lastMsgPath, req.Prompt), stream)
		},
		resume: func(id string) error {
			stream.userPrompt(e.resumePrompt)
			return e.run(ctx, e.buildResumeArgs(id, schemaPath, lastMsgPath), stream)
		},
		report:   func() (Verdict, error) { return parseVerdictFile(lastMsgPath) },
		session:  func() string { return stream.threadID },
		raw:      buf.String,
		usage:    func() TokenUsage { return stream.usage },
		rawUsage: func() string { return joinRawUsage(stream.rawUsage) },
	}.do()
}

// run drives one invocation and flushes the transcoder's trailing line, so a
// stream that ended without a final newline still renders before the report
// is read back. Mirrors the claude driver's seam of the same name.
func (e *codexEngine) run(ctx context.Context, args []string, stream *codexTranscoder) error {
	err := e.runCmd(ctx, args, stream)
	stream.Close()
	return err
}

// buildArgs assembles the codex exec invocation. Pure. The CLI contract
// (flag set, extra args, reporting instruction appended to the prompt) is
// pinned by table tests instead of live codex runs.
func (e *codexEngine) buildArgs(workDir, schemaPath, lastMsgPath, prompt string) []string {
	args := append([]string{"exec"}, e.modelArgs()...)
	args = append(args,
		"--json", // events, not prose: the prose trailer has no cache line
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
		"--json",
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

// parseVerdictFile reads and validates the agent's final-message report.
func parseVerdictFile(path string) (Verdict, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Verdict{}, err
	}
	return parseVerdict(data)
}
