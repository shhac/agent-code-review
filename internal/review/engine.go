// Package review runs the actual PR review. The engine is pluggable behind the
// Engine interface: the default "codex" driver shells out to `codex exec`; a
// "claude" driver can be added later. The Go side only assembles the prompt
// (main prompt + rule-derived fragments) and hands over tool access — the
// engine owns everything fuzzy: the review itself, the comment-only enforcement,
// and any post-approve Slack steps, all expressed in the prompt.
package review

import (
	"context"
	"fmt"

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

// Verdict decisions. The first four are the agent's final outcomes; WORKING
// is the agent's intermediate progress marker (the output schema constrains
// EVERY message, so progress notes need an honest value that doesn't
// overload SKIPPED — it is never a valid final report); ERROR is the
// driver's own value for "the invocation failed / no usable report".
const (
	DecisionApproved         = "APPROVED"
	DecisionCommented        = "COMMENTED"
	DecisionRequestedChanges = "REQUESTED_CHANGES" // the "reject" outcome
	DecisionSkipped          = "SKIPPED"
	DecisionWorking          = "WORKING"
	DecisionError            = "ERROR"
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

// NewEngine builds the configured engine. Only "codex" is wired today.
func NewEngine(cfg config.ReviewSettings) (Engine, error) {
	engine := cfg.Engine
	if engine == "" {
		engine = "codex"
	}
	switch engine {
	case "codex":
		return newCodex(cfg.Codex), nil
	default:
		return nil, fmt.Errorf("Unknown review engine: %q. Valid: codex", engine)
	}
}
