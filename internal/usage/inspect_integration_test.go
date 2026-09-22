//go:build integration

package usage

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/shhac/lib-agent-harness/session"
)

// TestFetchLive reads real headroom from each installed engine, through the
// harness, with the login already on this machine. No model is invoked. The
// failure worth catching is shape drift: a CLI whose reply no longer parses
// (ErrProtocol), or one that still meters but no longer reports the window
// IDs this package selects. A logged-out or unsupported engine is skipped.
// Run with: make test-integration
func TestFetchLive(t *testing.T) {
	for _, engine := range []string{"codex", "claude"} {
		t.Run(engine, func(t *testing.T) {
			if _, err := exec.LookPath(engine); err != nil {
				t.Skipf("%s not on PATH", engine)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()

			snap, err := Fetch(ctx, Source{Engine: engine})
			if errors.Is(err, session.ErrProtocol) {
				t.Fatalf("the CLI's usage reply no longer parses: %v", err)
			}
			if err != nil {
				t.Skipf("no headroom here: %v", err)
			}
			if !snap.OK() {
				t.Fatalf("the CLI metered, but none of its windows are the ones selected: %+v", snap)
			}
			for name, w := range map[string]*Window{"primary": snap.Primary, "secondary": snap.Secondary} {
				if w != nil && (w.UsedPercent < 0 || w.WindowMins <= 0) {
					t.Errorf("%s window = %+v", name, w)
				}
			}
			t.Logf("plan=%q primary=%+v secondary=%+v", snap.Plan, snap.Primary, snap.Secondary)
		})
	}
}
