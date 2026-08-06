package dashboard

import (
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/usage"
)

// usageView is pure, so the rules it encodes are testable without a poller or
// an HTTP round trip. Two of them matter: the floor consults the ACTIVE
// engine only (another engine's headroom must not pause or unpause the loop),
// and a daemon with no poller reports unavailable rather than implying
// headroom it never measured.
func TestUsageView(t *testing.T) {
	cfg := config.Config{Review: config.ReviewSettings{Engine: "codex"}}
	floored := usage.Snapshot{FetchedAt: time.Now(), Primary: &usage.Window{UsedPercent: 99, WindowMins: 300}}
	roomy := usage.Snapshot{FetchedAt: time.Now(), Primary: &usage.Window{UsedPercent: 1, WindowMins: 300}}

	t.Run("the floor follows the active engine", func(t *testing.T) {
		got := usageView(cfg, map[string]usage.Snapshot{"codex": floored, "claude": roomy}, 10, 5)
		if !got.ReviewPaused {
			t.Error("the active engine is floored, so reviews must report paused")
		}
		if got.PausedReason == "" {
			t.Error("a pause must say which window tripped")
		}
	})

	t.Run("another engine's floor does not pause the loop", func(t *testing.T) {
		got := usageView(cfg, map[string]usage.Snapshot{"codex": roomy, "claude": floored}, 10, 5)
		if got.ReviewPaused {
			t.Errorf("only the active engine's headroom may pause reviews, got %q", got.PausedReason)
		}
		if !got.Available {
			t.Error("a healthy active snapshot must report available")
		}
	})

	t.Run("no poller reports unavailable, not healthy", func(t *testing.T) {
		got := usageView(cfg, nil, 10, 5)
		if got.Available || got.ReviewPaused {
			t.Errorf("without a poller nothing is known: %+v", got)
		}
		if got.FreshTotal != 10 || got.Fresh24h != 5 {
			t.Errorf("token totals come from the store, not the poller: %+v", got)
		}
	})
}
