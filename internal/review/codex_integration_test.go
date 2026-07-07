//go:build integration

package review

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
)

// TestCodexSmoke drives the real codex CLI end-to-end through the driver: a
// trivial prompt that asks the agent to do nothing and report SKIPPED. Verifies
// the --output-schema / --output-last-message plumbing and verdict parsing
// against the actual binary. Run with: make test-integration
func TestCodexSmoke(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex not on PATH")
	}

	engine := newCodex(config.CodexSettings{Sandbox: "read-only"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	v, err := engine.Review(ctx, Request{
		Prompt:  "This is a plumbing smoke test. Do NOT review anything, do NOT run any commands, do NOT touch GitHub. Simply report that you skipped, with the summary \"smoke test\".",
		WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Review: %v (raw: %.500s)", err, v.Raw)
	}
	if v.Decision != DecisionSkipped {
		t.Errorf("decision = %q, want SKIPPED (summary: %s)", v.Decision, v.Summary)
	}
	if v.Summary == "" {
		t.Error("summary missing from verdict report")
	}
}
