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

// TestClaudeSmoke drives the real claude CLI end-to-end through the driver:
// a trivial prompt that asks the agent to do nothing and report SKIPPED.
// Verifies the --json-schema plumbing, the stream transcoding, and verdict
// parsing against the actual binary. Run with: make test-integration
//
// Deliberately pinned to the cheapest model: this spends real subscription
// quota, and the point is the plumbing, not the reasoning.
func TestClaudeSmoke(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not on PATH")
	}

	// Pinned to a static mode and the cheapest model: this exercises the
	// plumbing, not the classifier (TestClaudeAutoModeSmoke covers that, and
	// auto mode does not support haiku).
	engine := newClaude(config.ClaudeSettings{Model: "haiku", PermissionMode: "dontAsk"}, "NUDGE")
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
	if v.Tokens.Total() == 0 {
		t.Error("token usage must be read back from the result event")
	}
	if v.Tokens.Fresh() == 0 {
		t.Errorf("usage split = %+v, want the fresh half populated by a real run", v.Tokens)
	}
	// A live run always moves a large cached context, but WHICH cache column
	// it lands in depends only on how recently this suite last ran: cold, this
	// run populates the cache (CacheWrite); warm, a recent run already did and
	// it re-reads (CacheRead). Asserting CacheRead specifically made the test
	// pass or fail on cache timing rather than on the code, so it failed on
	// the first run of the day and passed on the second.
	//
	// What must hold either way is the thing the split exists for: the cached
	// context is parsed into a CACHE bucket rather than counted as fresh
	// input. Fresh input on a run like this is tens of tokens against tens of
	// thousands cached, so a comparison catches the miscategorisation the old
	// assertion was reaching for, without depending on the cache's state.
	cached := v.Tokens.CacheWrite + v.Tokens.CacheRead
	if cached == 0 {
		t.Errorf("usage split = %+v, want the cached context in a cache bucket", v.Tokens)
	}
	if v.Tokens.Input >= cached {
		t.Errorf("usage split = %+v: fresh input should be tiny beside the cached context, "+
			"so an Input this large means cache tokens were parsed into the wrong half", v.Tokens)
	}
	if v.UsageRaw == "" {
		t.Error("the verbatim usage payload must be kept, not just the projection")
	}

	// The transcript the dashboard tails must be the marker format, not the
	// raw NDJSON the CLI emitted.
	log, err := os.ReadFile(LogPath(workDir))
	if err != nil {
		t.Fatalf("agent log: %v", err)
	}
	transcript := string(log)
	if strings.Contains(transcript, `{"type":"assistant"`) {
		t.Error("agent.log holds raw stream JSON; the transcoder did not run")
	}
	for _, marker := range []string{"user\n", "claude\n", "tokens used\n"} {
		if !strings.Contains(transcript, marker) {
			t.Errorf("agent.log missing the %q marker:\n%.800s", strings.TrimSpace(marker), transcript)
		}
	}
}

// TestClaudeAutoModeSmoke exercises the DEFAULT permission mode against the
// real CLI. Auto mode has a failure shape the static modes don't: a blocked
// action has no user to prompt, so repeated blocks abort the run outright.
// That makes "the classifier lets an ordinary review action through" a
// property worth pinning against the live classifier rather than assuming.
//
// The workspace is a bare temp dir with no git remote, exactly as a real
// review gets it, because the classifier's notion of trusted infrastructure
// is seeded from the working directory.
func TestClaudeAutoModeSmoke(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not on PATH")
	}
	if _, err := exec.LookPath("gh"); err != nil {
		t.Skip("gh not on PATH")
	}

	// Pure defaults: this is the configuration a user gets from `config init`,
	// and the point is that the shipped default model and permission mode
	// actually work together against the live classifier.
	engine := newClaude(config.ClaudeSettings{}, "NUDGE")
	if engine.permissionMode != autoPermissionMode {
		t.Fatalf("permission mode = %q, want the %q default", engine.permissionMode, autoPermissionMode)
	}
	if engine.model != defaultModel {
		t.Fatalf("model = %q, want the %q default", engine.model, defaultModel)
	}
	if len(engine.allowedTools) != 0 {
		t.Fatalf("auto mode must ship no allow-list, got %v", engine.allowedTools)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	v, err := engine.Review(ctx, Request{
		Prompt: "This is a plumbing smoke test against a public repo. Run `gh pr list --repo cli/cli --limit 1` " +
			"to confirm read access works. Do NOT post anything, do NOT review anything. " +
			"Then report SKIPPED with the summary \"auto mode smoke test\".",
		WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Review: %v (raw: %.800s)", err, v.Raw)
	}
	if v.Decision != DecisionSkipped {
		t.Errorf("decision = %q, want SKIPPED (summary: %s)", v.Decision, v.Summary)
	}
	// The point of the test: gh ran without a static allow rule, which means
	// the classifier approved it.
	if !strings.Contains(v.Raw, "gh pr list") {
		t.Errorf("transcript shows no gh call; the classifier may have blocked it:\n%.800s", v.Raw)
	}
}
