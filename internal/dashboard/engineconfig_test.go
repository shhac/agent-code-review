package dashboard

import (
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/usage"
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

// The panel must show every engine, mark the active one, and explain an
// unavailable engine rather than rendering a blank meter. A failed poll
// stamps FetchedAt, so "available" cannot be derived from that alone.
func TestEngineUsagesReportsEveryEngine(t *testing.T) {
	now := time.Now()
	rows := engineUsages(map[string]usage.Snapshot{
		"codex":  {Error: `exec: "codex": executable file not found in $PATH`, FetchedAt: now},
		"claude": {Plan: "max", Primary: &usage.Window{UsedPercent: 8, WindowMins: 300}, FetchedAt: now},
	}, "claude")

	if len(rows) != len(review.Engines) {
		t.Fatalf("got %d rows, want one per wired engine (%d)", len(rows), len(review.Engines))
	}
	byEngine := map[string]engineUsage{}
	for _, r := range rows {
		byEngine[r.Engine] = r
	}
	if c := byEngine["codex"]; c.Available || c.Active || c.Error == "" {
		t.Errorf("codex row = %+v, want unavailable, inactive, with a reason", c)
	}
	if c := byEngine["claude"]; !c.Available || !c.Active || c.Usage == nil {
		t.Errorf("claude row = %+v, want available and marked active", c)
	}
}

// An engine that was never polled must still appear: a missing slot would
// read as "this engine does not exist" rather than "no data yet".
func TestEngineUsagesKeepsUnpolledEngines(t *testing.T) {
	rows := engineUsages(nil, "codex")
	if len(rows) != len(review.Engines) {
		t.Fatalf("got %d rows, want %d", len(rows), len(review.Engines))
	}
	for _, r := range rows {
		if r.Available || r.Error == "" {
			t.Errorf("%s = %+v, want unavailable with an explanation", r.Engine, r)
		}
	}
}
