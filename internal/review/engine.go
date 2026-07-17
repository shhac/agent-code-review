// Package review runs the actual PR review. The engine is pluggable behind the
// Engine interface: the default "codex" driver shells out to `codex exec`; a
// "claude" driver can be added later. The Go side only assembles the prompt
// (main prompt + rule-derived fragments) and hands over tool access; the
// engine owns everything fuzzy: the review itself, the comment-only enforcement,
// and any post-approve Slack steps, all expressed in the prompt.
package review

import (
	"context"
	"fmt"
	"strings"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
)

// Verdict is the agent's report of what it actually did for one PR. The agent
// performs the approve/comment on GitHub itself; this is the read-back so the
// store can record history and update status.
type Verdict struct {
	Decision   string `json:"decision"` // APPROVED | COMMENTED | REQUESTED_CHANGES | SKIPPED | ERROR
	Summary    string `json:"summary,omitempty"`
	Raw        string `json:"raw,omitempty"` // full engine transcript, for debugging
	TokensUsed int    `json:"-"`             // stream metadata, not part of the agent's report
}

// Verdict decisions, aliased from the store's canonical vocabulary (the
// layer both packages import, so the two sets cannot drift). The first four
// are the agent's final outcomes; WORKING is the agent's intermediate
// progress marker (the output schema constrains EVERY message, so progress
// notes need an honest value that doesn't overload SKIPPED; it is never a
// valid final report); ERROR is the driver's own value for "the invocation
// failed / no usable report".
const (
	DecisionApproved         = store.VerdictApproved
	DecisionCommented        = store.VerdictCommented
	DecisionRequestedChanges = store.VerdictRequestedChanges // the "reject" outcome
	DecisionSkipped          = store.VerdictSkipped
	DecisionWorking          = store.VerdictWorking
	DecisionError            = store.VerdictError
)

// Request is one PR review job.
type Request struct {
	Candidate store.Candidate
	Prompt    string // fully assembled instructions
	WorkDir   string // tmp workspace the engine may use
}

// Provenance identifies the engine configuration that produced an outcome.
// Empty fields mean that an engine does not expose that detail.
type Provenance struct {
	Engine       string
	Model        string
	Effort       string
	CodexVersion string
}

// Engine reviews a single PR and owns the provenance recorded for it. This
// keeps driver-specific settings out of the scheduler's lifecycle code.
type Engine interface {
	Review(ctx context.Context, req Request) (Verdict, error)
	Provenance(ctx context.Context) Provenance
}

// Engines lists the wired review engines, default first: the one vocabulary
// behind NewEngine's dispatch and error text and the CLI's validation and
// completion. (config.Engine()'s display default must restate the first
// entry — config cannot import review without a cycle; TestNewEngine pins
// the two in step.)
var Engines = []string{"codex"}

// NewEngine builds the configured engine.
func NewEngine(cfg config.ReviewSettings) (Engine, error) {
	engine := cfg.Engine
	if engine == "" {
		engine = Engines[0]
	}
	switch engine {
	case "codex":
		return newCodex(cfg.Codex, ResumePrompt(cfg)), nil
	default:
		return nil, fmt.Errorf("Unknown review engine: %q. Valid: %s", engine, strings.Join(Engines, ", "))
	}
}
