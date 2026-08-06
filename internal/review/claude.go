package review

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"strconv"
	"strings"

	"github.com/shhac/agent-code-review/internal/config"
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

func newClaude(c config.ClaudeSettings, resumePrompt string) *claudeEngine {
	bin := c.Bin
	if bin == "" {
		bin = "claude"
	}
	mode := c.PermissionMode
	if mode == "" {
		mode = defaultPermissionMode
	}
	model := c.Model
	if model == "" {
		model = defaultModel
	}
	effort := c.Effort
	if effort == "" {
		effort = defaultEffort
	}
	tools := c.AllowedTools
	if len(tools) == 0 && mode != autoPermissionMode {
		tools = fallbackAllowedTools
	}
	e := &claudeEngine{
		bin: bin, model: model, effort: effort, permissionMode: mode,
		allowedTools: tools, maxBudgetUSD: c.MaxBudgetUSD, args: c.Args,
		maxResumes: resolveMaxResumes(c.MaxResumes), resumePrompt: resumePrompt,
	}
	e.runCmd = e.execClaude
	return e
}

// execClaude is the production runCmd: one claude subprocess rooted at
// workDir, stdout into the transcoder, stderr into the log sink verbatim
// (claude prints plain warnings there, not JSON).
func (e *claudeEngine) execClaude(ctx context.Context, args []string, workDir string, stream, sink io.Writer) error {
	cmd := exec.CommandContext(ctx, e.bin, args...)
	cmd.Dir = workDir // claude has no --cd; the workdir IS the process cwd
	cmd.Stdout = stream
	cmd.Stderr = sink
	// Out of the terminal's process group, so a graceful Ctrl-C cannot kill a
	// review mid-run. Only reviewCtx (the SECOND signal) ends this.
	detachFromTerminalSignals(cmd)
	return cmd.Run()
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
	out, err := exec.CommandContext(ctx, e.bin, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
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
	stream := newStreamTranscoder(sink)

	return resumableRun{
		engine: "claude -p",
		max:    e.maxResumes,
		start: func() error {
			// An interrupted attempt left a live session; continuing it costs
			// the nudge instead of the whole review again.
			if req.ResumeSession != "" {
				stream.userPrompt(e.resumePrompt)
				return e.run(ctx, e.buildResumeArgs(req.ResumeSession), workDir, stream, sink)
			}
			stream.userPrompt(req.Prompt)
			return e.run(ctx, e.buildArgs(req.Prompt), workDir, stream, sink)
		},
		resume: func(id string) error {
			stream.userPrompt(e.resumePrompt)
			return e.run(ctx, e.buildResumeArgs(id), workDir, stream, sink)
		},
		report:   stream.verdict,
		session:  func() string { return stream.sessionID },
		raw:      buf.String,
		cost:     func() float64 { return stream.costUSD },
		usage:    func() TokenUsage { return stream.usage },
		rawUsage: func() string { return joinRawUsage(stream.rawUsage) },
	}.do()
}

// run drives one invocation and flushes the transcoder's trailing line, so a
// stream that ended without a final newline still renders before the report
// is read back.
func (e *claudeEngine) run(ctx context.Context, args []string, workDir string, stream *streamTranscoder, sink io.Writer) error {
	err := e.runCmd(ctx, args, workDir, stream, sink)
	stream.Close()
	return err
}

// inlineVerdictSchema is the shared schema on one line: --json-schema takes
// the schema itself, where codex's --output-schema takes a path to it.
var inlineVerdictSchema = jsonCompact(verdictSchema)

// buildArgs assembles the claude invocation. Pure, and pinned by table tests
// rather than live runs, exactly like the codex driver's.
func (e *claudeEngine) buildArgs(prompt string) []string {
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--json-schema", inlineVerdictSchema}
	args = append(args, e.commonArgs()...)
	return appendPositionals(args, prompt+reportingInstruction)
}

// buildResumeArgs assembles the invocation that nudges a session which ended
// without a final report. --resume restores the session's own context, so
// only the run-shaping flags are repeated.
func (e *claudeEngine) buildResumeArgs(sessionID string) []string {
	args := []string{"-p", "--resume", sessionID, "--output-format", "stream-json", "--verbose", "--json-schema", inlineVerdictSchema}
	args = append(args, e.commonArgs()...)
	return appendPositionals(args, e.resumePrompt)
}

// commonArgs are the flags both the initial and resumed invocations carry.
func (e *claudeEngine) commonArgs() []string {
	var args []string
	if e.model != "" {
		args = append(args, "--model", e.model)
	}
	if e.effort != "" {
		args = append(args, "--effort", e.effort)
	}
	if e.permissionMode != "" {
		args = append(args, "--permission-mode", e.permissionMode)
	}
	if len(e.allowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(e.allowedTools, ","))
	}
	if e.maxBudgetUSD > 0 {
		// A hard per-invocation ceiling with no codex equivalent. On a
		// subscription the figure is the notional API-rate valuation of the
		// run, so this bounds runaway reviews rather than literal spend.
		args = append(args, "--max-budget-usd", strconv.FormatFloat(e.maxBudgetUSD, 'f', -1, 64))
	}
	return append(args, e.args...)
}

// jsonCompact keeps the inline schema on one argv entry regardless of how the
// constant is formatted in source.
func jsonCompact(s string) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(s)); err != nil {
		return s
	}
	return buf.String()
}
