package review

import (
	"cmp"

	"github.com/shhac/crew-code-review/internal/config"
	"github.com/shhac/lib-agent-harness/native"
)

// newClaude drives `claude -p` non-interactively with the assembled prompt.
//
// There is no Go Agent SDK; the documented way to drive Claude Code from
// another language is this CLI surface, which is also what the Python and
// TypeScript SDKs spawn underneath. Two shape differences from codex are worth
// knowing: the report arrives in the output stream rather than a file
// (--json-schema, read from the result event's structured_output), and there
// is no --cd flag, so the workspace is set as the process working directory.
func newClaude(c config.ClaudeSettings, resumePrompt string) *nativeEngine {
	return &nativeEngine{
		label:        "claude -p",
		cfg:          resolveClaude(c),
		maxResumes:   resolveMaxResumes(c.MaxResumes),
		resumePrompt: resumePrompt,
		template:     claudeTemplate,
	}
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
const defaultModel = "claude-opus-5-5"

// defaultEffort is pinned for the same reason as the model, plus one specific
// to this engine: the run reports no effort back, so an unpinned effort is
// also an UNRECORDED one, and history could not tell you which effort
// produced which cost.
//
// medium, which is also Opus 5.5's own default, rather than the xhigh that
// general coding-and-agentic guidance suggests, because review is the workload
// that guidance is least true of: code review holds both precision and recall
// at lower effort, so the extra spend buys little here. If review quality
// slips, this is the first dial to raise.
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

// resolveClaude applies every claude default in one place, so the engine that
// RUNS and the preflight that JUDGES agree about what will run.
//
// They did not. Preflight defaulted the permission mode itself and then handed
// the RAW model to claudeAutoModeSupports, which defaulted the model a layer
// down — so the check most worth trusting, the auto-mode/model pairing that
// makes every review fail, was reasoning about a configuration one step
// removed from the one newClaude would build.
func resolveClaude(c config.ClaudeSettings) native.Config {
	cfg := native.Config{
		Engine:         "claude",
		Binary:         config.DefaultBin("claude", c.Bin),
		Model:          cmp.Or(c.Model, defaultModel),
		Effort:         cmp.Or(c.Effort, defaultEffort),
		PermissionMode: cmp.Or(c.PermissionMode, defaultPermissionMode),
		AllowedTools:   c.AllowedTools,
		MaxBudgetUSD:   c.MaxBudgetUSD,
		Args:           c.Args,
	}
	// Auto mode routes every action through the classifier, so a fallback list
	// would narrow what it may do rather than widen it.
	if len(cfg.AllowedTools) == 0 && cfg.PermissionMode != autoPermissionMode {
		cfg.AllowedTools = fallbackAllowedTools
	}
	return cfg
}

// claudeTemplate passes the schema inline; the report comes back in the
// stream, so nothing is written into the workspace. Claude never reads a
// schema file, and a workspace that could not hold one used to fail its
// reviews over it.
func claudeTemplate(string) (native.Request, error) {
	return native.Request{Schema: verdictSchema}, nil
}
