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
	Decision string `json:"decision"` // APPROVED | COMMENTED | SKIPPED | ERROR
	Summary  string `json:"summary,omitempty"`
	Raw      string `json:"raw,omitempty"` // full engine transcript, for debugging
}

// Verdict decisions. The first three are reported by the agent; ERROR is the
// driver's own value for "the invocation failed / no usable report".
const (
	DecisionApproved  = "APPROVED"
	DecisionCommented = "COMMENTED"
	DecisionSkipped   = "SKIPPED"
	DecisionError     = "ERROR"
)

// Request is one PR review job.
type Request struct {
	Candidate store.Candidate
	Prompt    string // fully assembled instructions
	WorkDir   string // tmp workspace the engine may use
}

// Engine reviews a single PR.
type Engine interface {
	Name() string
	Review(ctx context.Context, req Request) (Verdict, error)
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
