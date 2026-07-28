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
	// CostUSD is the run's API-rate valuation as the engine reported it, not
	// money charged: on a subscription it is what the tokens would have cost
	// at API rates. 0 when the engine reports no cost (codex prints only a
	// token trailer). Like TokensUsed, it is stream metadata rather than
	// something the agent claims.
	CostUSD float64 `json:"-"`
	// Tokens is the run's usage split by kind, when the engine reports one.
	// Zero across the board means it reported only a total (codex does), in
	// which case TokensUsed is all there is.
	Tokens TokenUsage `json:"-"`
}

// TokenUsage splits a run's tokens by how they were spent. The distinction
// that matters is CacheRead: context the model re-read rather than processed
// fresh. It dominates a long agentic session — a review can re-read millions
// of cached tokens across its turns — so a total that includes it is an order
// of magnitude larger than one that doesn't, and the two are not comparable
// between engines that report differently.
type TokenUsage struct {
	Input         int
	Output        int
	CacheCreation int
	CacheRead     int
}

// Total is every token the run moved, cached reads included.
func (t TokenUsage) Total() int {
	return t.Input + t.Output + t.CacheCreation + t.CacheRead
}

// Fresh is the tokens actually processed: everything except context re-read
// from cache. This is the figure comparable across engines.
func (t TokenUsage) Fresh() int {
	return t.Input + t.Output + t.CacheCreation
}

// Reported says whether the engine gave a breakdown at all.
func (t TokenUsage) Reported() bool { return t.Total() > 0 }

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
	Engine        string
	Model         string
	Effort        string
	EngineVersion string // the engine CLI's own version, however it reports it
}

// Engine reviews a single PR and owns the provenance recorded for it. This
// keeps driver-specific settings out of the scheduler's lifecycle code.
type Engine interface {
	Review(ctx context.Context, req Request) (Verdict, error)
	Provenance(ctx context.Context) Provenance
}

// Engines lists the wired review engines, default first: the one vocabulary
// behind NewEngine's dispatch and error text and the CLI's validation and
// completion. Defined in config (which this package already imports) so that
// config.Engine()'s default and this list cannot disagree; re-exported here
// because "which engines exist" reads as a review concept at the call sites.
var Engines = config.EngineNames

// NewEngine builds the configured engine.
func NewEngine(cfg config.ReviewSettings) (Engine, error) {
	engine := cfg.Engine
	if engine == "" {
		engine = Engines[0]
	}
	switch engine {
	case "codex":
		return newCodex(cfg.Codex, ResumePrompt(cfg)), nil
	case "claude":
		return newClaude(cfg.Claude, ResumePrompt(cfg)), nil
	default:
		return nil, fmt.Errorf("Unknown review engine: %q. Valid: %s", engine, strings.Join(Engines, ", "))
	}
}
