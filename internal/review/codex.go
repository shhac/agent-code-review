package review

import (
	"context"
	"io"
	"path/filepath"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/lib-agent-harness/native"
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
	bin := config.DefaultBin("codex", c.Bin)
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
	return native.Execute(ctx, e.harnessConfig(), args, "", sink, sink)
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
	version, _ := native.Version(ctx, e.harnessConfig())
	return version
}

func (e *codexEngine) Review(ctx context.Context, req Request) (Verdict, error) {
	workDir, err := prepareWorkspace(req.WorkDir)
	if err != nil {
		return Verdict{Decision: DecisionError}, err
	}
	schemaPath, err := writeVerdictSchema(workDir)
	if err != nil {
		return Verdict{Decision: DecisionError}, err
	}
	lastMsgPath := filepath.Join(workDir, "verdict.json")

	sink, buf, closeSink := newAgentSink(workDir)
	defer closeSink()
	stream, _ := native.NewStream("codex", sink, native.StreamOptions{Structured: true})
	var latest native.Result
	invoke := func(r native.Request) error {
		cfg := e.harnessConfig()
		cfg.RunCommand = func(ctx context.Context, args []string, _ string, stdout, stderr io.Writer) error {
			return e.runCmd(ctx, args, stdout)
		}
		var err error
		latest, err = native.Run(ctx, cfg, r, stream)
		return err
	}

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
				return invoke(native.Request{ResumeSession: req.ResumeSession, SchemaPath: schemaPath, OutputPath: lastMsgPath, Prompt: e.resumePrompt})
			}
			return invoke(native.Request{WorkDir: workDir, SchemaPath: schemaPath, OutputPath: lastMsgPath, Prompt: req.Prompt + reportingInstruction})
		},
		resume: func(id string) error {
			return invoke(native.Request{ResumeSession: id, SchemaPath: schemaPath, OutputPath: lastMsgPath, Prompt: e.resumePrompt})
		},
		report: func() (Verdict, error) { return parseVerdict(latest.Report) },
		raw:    buf.String,
		stream: stream,
	}.do()
}

func (e *codexEngine) harnessConfig() native.Config {
	return native.Config{Engine: "codex", Binary: e.bin, Model: e.model, Effort: e.effort, Sandbox: e.sandbox, Args: e.args}
}
func (e *codexEngine) buildArgs(workDir, schemaPath, lastMsgPath, prompt string) []string {
	args, _ := native.Args(e.harnessConfig(), native.Request{WorkDir: workDir, SchemaPath: schemaPath, OutputPath: lastMsgPath, Prompt: prompt + reportingInstruction})
	return args
}
func (e *codexEngine) buildResumeArgs(sessionID, schemaPath, lastMsgPath string) []string {
	args, _ := native.Args(e.harnessConfig(), native.Request{ResumeSession: sessionID, SchemaPath: schemaPath, OutputPath: lastMsgPath, Prompt: e.resumePrompt})
	return args
}
