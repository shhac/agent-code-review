//go:build integration

package review

import (
	"context"
	"os"
	"os/exec"
	"strings"
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

	engine := newCodex(config.CodexSettings{Sandbox: "read-only"}, "NUDGE")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	workDir := t.TempDir()
	v, err := engine.Review(ctx, Request{
		Prompt:  "This is a plumbing smoke test. Do NOT review anything, do NOT run any commands, do NOT touch GitHub. Simply report that you skipped, with the summary \"smoke test\".",
		WorkDir: workDir,
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
	// Under --json the token split comes off turn.completed, and the transcript
	// is rendered by our transcoder rather than printed by codex. Both are new
	// responsibilities, so both are checked against the real binary.
	if v.Tokens.Input == 0 || v.Tokens.Total() == 0 {
		t.Errorf("usage split = %+v, want a real run's figures", v.Tokens)
	}
	if v.UsageRaw == "" {
		t.Error("the verbatim usage payload must be kept, not just the projection")
	}

	log, err := os.ReadFile(LogPath(workDir))
	if err != nil {
		t.Fatalf("agent log: %v", err)
	}
	transcript := string(log)
	if strings.Contains(transcript, `"type":"turn.completed"`) {
		t.Error("raw NDJSON leaked into the log; it must be the marker format the dashboard parses")
	}
	for _, marker := range []string{"session id: ", "\nuser\n", "\ncodex\n", "\ntokens used\n"} {
		if !strings.Contains(transcript, marker) {
			t.Errorf("transcript missing %q\n--- got ---\n%.1200s", marker, transcript)
		}
	}
}
