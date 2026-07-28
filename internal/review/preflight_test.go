package review

import (
	"strings"
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
)

// The pairing that motivated this check: nothing rejects it at config time,
// and every review then fails identically at run time.
func TestPreflightCatchesAutoModeModelMismatch(t *testing.T) {
	problems := Preflight(config.ReviewSettings{
		Engine: "claude",
		Claude: config.ClaudeSettings{Model: "haiku"}, // auto mode is the default
	})
	if len(problems) != 1 || !strings.Contains(problems[0], "not supported") {
		t.Fatalf("problems = %v, want the auto-mode model mismatch", problems)
	}
}

// Auto mode is the only mode the mismatch applies to; pairing an unsupported
// model with a static mode is a legitimate configuration.
func TestPreflightAllowsUnsupportedModelInStaticMode(t *testing.T) {
	if problems := Preflight(config.ReviewSettings{
		Engine: "claude",
		Claude: config.ClaudeSettings{Model: "haiku", PermissionMode: "dontAsk"},
	}); len(problems) != 0 {
		t.Errorf("problems = %v, want none for a static mode", problems)
	}
}

// An allow-list in auto mode routes those tools around the classifier, which
// silently weakens exactly what the mode exists to provide.
func TestPreflightFlagsAllowListInAutoMode(t *testing.T) {
	problems := Preflight(config.ReviewSettings{
		Engine: "claude",
		Claude: config.ClaudeSettings{AllowedTools: []string{"Bash(gh *)"}},
	})
	if len(problems) != 1 || !strings.Contains(problems[0], "bypass") {
		t.Errorf("problems = %v, want the allow-list warning", problems)
	}
}

func TestPreflightCleanConfigs(t *testing.T) {
	for name, cfg := range map[string]config.ReviewSettings{
		"codex":            {Engine: "codex"},
		"engine unset":     {},
		"claude defaults":  {Engine: "claude"},
		"claude pinned ok": {Engine: "claude", Claude: config.ClaudeSettings{Model: "claude-sonnet-5"}},
	} {
		if problems := Preflight(cfg); len(problems) != 0 {
			t.Errorf("%s: problems = %v, want none", name, problems)
		}
	}
}

// The engine default must itself pass the check it enforces, or a fresh
// install would be broken out of the box.
func TestDefaultModelPassesAutoMode(t *testing.T) {
	if !claudeAutoModeSupports("") {
		t.Errorf("the %q default is rejected by auto mode", defaultModel)
	}
}
