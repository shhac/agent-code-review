package review

import (
	"cmp"
	"context"
	"io"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/lib-agent-harness/native"
)

// claudeEngine invokes `claude -p` non-interactively with the assembled
// prompt. It is the same bargain as the codex driver: the agent performs the
// review itself (posting to GitHub, running any post-approve steps) and
// reports back what it did through the shared verdict schema, which this
// driver reads out of the run's structured output. The engine never posts the
// review.
//
// There is no Go Agent SDK; the documented way to drive Claude Code from
// another language is this CLI surface, which is also what the Python and
// TypeScript SDKs spawn underneath. Two shape differences from codex are worth
// knowing: the report arrives in the output stream rather than a file
// (--json-schema, read from the result event's structured_output), and there
// is no --cd flag, so the workspace is set as the process working directory.
type claudeEngine struct {
	bin            string
	model          string
	effort         string
	permissionMode string
	allowedTools   []string
	maxBudgetUSD   float64
	args           []string
	maxResumes     int
	resumePrompt   string

	// runCmd launches one claude invocation with its stdout streamed into
	// stream (the transcoder) and its stderr into sink: the engine's only
	// subprocess seam. Production execs e.bin from workDir; tests inject a
	// recorder that writes a canned stream, so the resume loop and outcome
	// precedence test in-process exactly as codex's do.
	runCmd func(ctx context.Context, args []string, workDir string, stream, sink io.Writer) error
}

// autoPermissionMode routes each action through Claude Code's classifier
// instead of a static allow-list. It is the default because a review is
// open-ended tool work: the prompt may reach for gh, a language toolchain, or
// any agent-* CLI the user has set up, and enumerating that up front defeats
// the point of expressing review behaviour as prompt.
//
// It also fits the threat model better than a wider allow-list would. A PR's
// diff, description, and comments are untrusted input, and the classifier is
// built for exactly that: it reads user messages, tool calls, and CLAUDE.md,
// but tool RESULTS are stripped, so instructions smuggled into a PR cannot
// talk it into approving an action. A blanket allow rule has no such
// property.
const autoPermissionMode = "auto"

// defaultPermissionMode is what the engine runs in when config says nothing.
const defaultPermissionMode = autoPermissionMode

// defaultModel pins the review model rather than inheriting whatever the
// account's session default happens to be, so a review's cost and depth do
// not silently change when that default moves. A full id, not the `opus`
// alias, for the same reason and to match how codex.model is pinned.
//
// It must also stay a model auto mode supports (Opus 4.6+, Sonnet 4.6+, or
// Fable 5), since auto is this engine's default permission mode.
const defaultModel = "claude-opus-5"

// defaultEffort is pinned for the same reason as the model, plus one specific
// to this engine: the run reports no effort back, so an unpinned effort is
// also an UNRECORDED one, and history could not tell you which effort
// produced which cost.
//
// medium rather than the xhigh that general Opus 5 coding-and-agentic
// guidance suggests, because review is the workload that guidance is least
// true of: on this model code review holds both precision and recall at lower
// effort, so the extra spend buys little here. If review quality slips, this
// is the first dial to raise.
const defaultEffort = "medium"

// fallbackAllowedTools is the floor a review cannot run without in the
// STATIC modes. acceptEdits and dontAsk cover reads and file writes but NOT
// arbitrary shell, so without an explicit allow rule the run aborts the first
// time the agent reaches for gh. gh is the one CLI this tool assumes, so
// allowing it is part of the engine working at all rather than an
// environment-specific choice.
//
// Deliberately NOT applied in auto mode. Allow rules resolve ahead of the
// classifier, so shipping `Bash(gh *)` there would route the one command that
// can merge, close, and post around the very judgment the mode exists to
// provide. In auto mode the classifier is the mechanism; an empty list is the
// correct default.
var fallbackAllowedTools = []string{"Bash(gh *)", "Read", "Glob", "Grep"}

// resolvedClaude applies every claude default in one place, so the engine that
// RUNS and the preflight that JUDGES agree about what will run.
//
// They did not. Preflight defaulted the permission mode itself and then handed
// the RAW model to claudeAutoModeSupports, which defaulted the model a layer
// down — so the check most worth trusting, the auto-mode/model pairing that
// makes every review fail, was reasoning about a configuration one step
// removed from the one newClaude would build.
type resolvedClaude struct {
	bin, model, effort, permissionMode string
	allowedTools                       []string
}

func resolveClaude(c config.ClaudeSettings) resolvedClaude {
	r := resolvedClaude{
		bin:            config.DefaultBin("claude", c.Bin),
		model:          cmp.Or(c.Model, defaultModel),
		effort:         cmp.Or(c.Effort, defaultEffort),
		permissionMode: cmp.Or(c.PermissionMode, defaultPermissionMode),
		allowedTools:   c.AllowedTools,
	}
	// Auto mode routes every action through the classifier, so a fallback list
	// would narrow what it may do rather than widen it.
	if len(r.allowedTools) == 0 && r.permissionMode != autoPermissionMode {
		r.allowedTools = fallbackAllowedTools
	}
	return r
}

func newClaude(c config.ClaudeSettings, resumePrompt string) *claudeEngine {
	r := resolveClaude(c)
	e := &claudeEngine{
		bin: r.bin, model: r.model, effort: r.effort, permissionMode: r.permissionMode,
		allowedTools: r.allowedTools, maxBudgetUSD: c.MaxBudgetUSD, args: c.Args,
		maxResumes: resolveMaxResumes(c.MaxResumes), resumePrompt: resumePrompt,
	}
	e.runCmd = e.execClaude
	return e
}

// execClaude is the production runCmd: one claude subprocess rooted at
// workDir, stdout into the transcoder, stderr into the log sink verbatim
// (claude prints plain warnings there, not JSON).
func (e *claudeEngine) execClaude(ctx context.Context, args []string, workDir string, stream, sink io.Writer) error {
	return native.Execute(ctx, e.harnessConfig(), args, workDir, stream, sink)
}

func (e *claudeEngine) Name() string { return "claude" }

func (e *claudeEngine) Provenance(ctx context.Context) Provenance {
	return Provenance{Engine: e.Name(), Model: e.model, Effort: e.effort, EngineVersion: e.claudeVersion(ctx)}
}

// claudeVersion probes `claude --version` uncached, for the same reason the
// codex driver does: the engine is rebuilt from live config every cycle and a
// review takes minutes, so one cheap exec keeps the recorded version accurate
// across a mid-cycle upgrade. "" on a failed probe.
func (e *claudeEngine) claudeVersion(ctx context.Context) string {
	version, _ := native.Version(ctx, e.harnessConfig())
	return version
}

func (e *claudeEngine) Review(ctx context.Context, req Request) (Verdict, error) {
	workDir, err := prepareWorkspace(req.WorkDir)
	if err != nil {
		return Verdict{Decision: DecisionError}, err
	}

	sink, buf, closeSink := newAgentSink(workDir)
	defer closeSink()

	// One transcoder spans every invocation of the review, so a resumed run
	// keeps appending to the same transcript and its token totals accumulate
	// the way codex's repeated trailers do.
	stream, _ := native.NewStream("claude", sink, native.StreamOptions{Structured: true})
	var latest native.Result
	invoke := func(r native.Request) error {
		cfg := e.harnessConfig()
		cfg.RunCommand = e.runCmd
		var err error
		latest, err = native.Run(ctx, cfg, r, stream)
		return err
	}

	return resumableRun{
		engine: "claude -p",
		max:    e.maxResumes,
		start: func() error {
			// An interrupted attempt left a live session; continuing it costs
			// the nudge instead of the whole review again.
			if req.ResumeSession != "" {
				return invoke(native.Request{WorkDir: workDir, ResumeSession: req.ResumeSession, Schema: verdictSchema, Prompt: e.resumePrompt})
			}
			return invoke(native.Request{WorkDir: workDir, Schema: verdictSchema, Prompt: req.Prompt + reportingInstruction})
		},
		resume: func(id string) error {
			return invoke(native.Request{WorkDir: workDir, ResumeSession: id, Schema: verdictSchema, Prompt: e.resumePrompt})
		},
		report: func() (Verdict, error) {
			if len(latest.Report) == 0 {
				_, err := stream.Report()
				return Verdict{}, err
			}
			return parseVerdict(latest.Report)
		},
		session:  func() string { return stream.Snapshot().SessionID },
		raw:      buf.String,
		cost:     func() float64 { return stream.Snapshot().CostUSD },
		usage:    func() TokenUsage { return stream.Snapshot().Usage },
		rawUsage: func() string { return stream.Snapshot().RawUsage },
	}.do()
}

func (e *claudeEngine) harnessConfig() native.Config {
	return native.Config{Engine: "claude", Binary: e.bin, Model: e.model, Effort: e.effort, PermissionMode: e.permissionMode, AllowedTools: e.allowedTools, MaxBudgetUSD: e.maxBudgetUSD, Args: e.args}
}
func (e *claudeEngine) buildArgs(prompt string) []string {
	args, _ := native.Args(e.harnessConfig(), native.Request{Prompt: prompt + reportingInstruction, Schema: verdictSchema})
	return args
}
func (e *claudeEngine) buildResumeArgs(sessionID string) []string {
	args, _ := native.Args(e.harnessConfig(), native.Request{Prompt: e.resumePrompt, ResumeSession: sessionID, Schema: verdictSchema})
	return args
}
