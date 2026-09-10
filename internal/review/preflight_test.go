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
		Claude: config.ClaudeSettings{EngineCommon: config.EngineCommon{Model: "haiku"}}, // auto mode is the default
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
		Claude: config.ClaudeSettings{EngineCommon: config.EngineCommon{Model: "haiku"}, PermissionMode: "dontAsk"},
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
		"claude pinned ok": {Engine: "claude", Claude: config.ClaudeSettings{EngineCommon: config.EngineCommon{Model: "claude-sonnet-5"}}},
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

// TestPreflightJudgesWhatWillActuallyRun is the property the shared resolution
// exists for. Preflight used to default the permission mode itself and hand
// the RAW model to the auto-mode check, which defaulted the model a layer
// down — so it reasoned about a configuration one step removed from the one
// newClaude builds, on the check that matters most.
func TestPreflightJudgesWhatWillActuallyRun(t *testing.T) {
	cases := map[string]config.ClaudeSettings{
		"model and mode both left to their defaults": {},
		"mode defaulted, model pinned to a good one": {EngineCommon: config.EngineCommon{Model: "claude-opus-5"}},
		"mode pinned to auto, model defaulted":       {PermissionMode: "auto"},
	}
	for name, cs := range cases {
		cfg := config.ReviewSettings{Engine: "claude", Claude: cs}
		got := Preflight(cfg)
		e := newClaude(cs, "")
		// Whatever preflight concluded, it must have concluded it about the
		// engine's own resolved values.
		r := resolveClaude(cs)
		if r.model != e.model || r.permissionMode != e.permissionMode {
			t.Errorf("%s: preflight resolved %q/%q, engine built %q/%q",
				name, r.model, r.permissionMode, e.model, e.permissionMode)
		}
		if len(got) != 0 {
			t.Errorf("%s: a supported default pairing must raise nothing, got %v", name, got)
		}
	}

	// And the trap it is for still fires, on the resolved pair.
	bad := config.ReviewSettings{Engine: "claude", Claude: config.ClaudeSettings{
		EngineCommon: config.EngineCommon{Model: "haiku"}, PermissionMode: "auto",
	}}
	if len(Preflight(bad)) == 0 {
		t.Error("auto mode against a model that cannot run it must still be caught")
	}
}
