package cli

import (
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/review"
)

// The daemon meters every wired engine, so this list must be derived from the
// engine roster rather than restated. A hand-written list would silently skip
// a third engine: no compile error, no failing test, just no usage polled.
func TestUsageSourcesCoversEveryWiredEngine(t *testing.T) {
	cfg := config.Config{Review: config.ReviewSettings{
		Codex:  config.CodexSettings{Bin: "codex-dev"},
		Claude: config.ClaudeSettings{Bin: "claude-dev"},
	}}
	got := usageSources(cfg)
	if len(got) != len(review.Engines) {
		t.Fatalf("got %d sources, want one per wired engine (%d)", len(got), len(review.Engines))
	}
	bins := map[string]string{}
	for _, src := range got {
		bins[src.Engine] = src.Bin
	}
	for _, engine := range review.Engines {
		if _, ok := bins[engine]; !ok {
			t.Errorf("engine %q is wired but never metered", engine)
		}
	}
	if bins["codex"] != "codex-dev" || bins["claude"] != "claude-dev" {
		t.Errorf("bins = %v, want each engine's configured binary", bins)
	}
}
