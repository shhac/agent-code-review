//go:build integration

package usage

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// TestClaudeUsageLive reads real subscription headroom through the whole
// path: stored credential, OAuth endpoint, Snapshot mapping, plan probe.
// This is the piece that cannot be unit-tested, because it depends on an
// undocumented endpoint that may change. Run with: make test-integration
func TestClaudeUsageLive(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not on PATH")
	}
	if _, err := claudeOAuthToken(); err != nil {
		t.Skipf("no stored claude credential: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	snap, err := fetchClaude(ctx, "claude")
	if err != nil {
		t.Fatalf("fetchClaude: %v", err)
	}
	if snap.Primary == nil {
		t.Fatal("no session window: the endpoint's shape has changed")
	}
	if snap.Primary.WindowMins != claudeSessionMins || snap.Primary.ResetsAt == 0 {
		t.Errorf("session window = %+v", snap.Primary)
	}
	if snap.Secondary == nil || snap.Secondary.WindowMins != claudeWeeklyMins {
		t.Errorf("weekly window = %+v", snap.Secondary)
	}
	if snap.Plan == "" {
		t.Error("plan probe returned nothing; `claude auth status --json` may have changed")
	}
	// Utilization is a percentage; anything outside the range means the field
	// stopped meaning what BelowFloor assumes it means.
	for name, w := range map[string]*Window{"session": snap.Primary, "weekly": snap.Secondary} {
		if w != nil && (w.UsedPercent < 0 || w.UsedPercent > 100) {
			t.Errorf("%s used percent = %v, outside 0-100", name, w.UsedPercent)
		}
	}
	t.Logf("plan=%s session=%.0f%% used weekly=%.0f%% used", snap.Plan, snap.Primary.UsedPercent, snap.Secondary.UsedPercent)
}
