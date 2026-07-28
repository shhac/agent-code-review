package dashboard

import (
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
)

// The dashboard must report the dials of whichever engine is configured;
// always reading codex's would show settings that no review will use.
func TestEngineConfigFollowsConfiguredEngine(t *testing.T) {
	cfg := config.Config{Review: config.ReviewSettings{
		Codex:  config.CodexSettings{Model: "gpt-5.6", Effort: "high"},
		Claude: config.ClaudeSettings{Model: "claude-opus-5", Effort: "medium"},
	}}

	if got := engineConfigOf(cfg); got.Model != "gpt-5.6" || got.Effort != "high" {
		t.Errorf("unset engine defaults to codex, got %+v", got)
	}
	cfg.Review.Engine = "claude"
	if got := engineConfigOf(cfg); got.Model != "claude-opus-5" || got.Effort != "medium" {
		t.Errorf("claude engine = %+v, want claude's dials", got)
	}
}
