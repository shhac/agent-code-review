package config

import "testing"

// TestEngineCommonIsTheOnlyEngineSwitch pins the property the shared dials
// exist for: adding an engine should be one case in one function, not a hunt
// through every getter that used to write `if engine == "claude"`.
func TestEngineCommonIsTheOnlyEngineSwitch(t *testing.T) {
	c := Config{Review: ReviewSettings{
		Engine: "claude",
		Codex:  CodexSettings{EngineCommon: EngineCommon{Bin: "codex-dev", Model: "gpt", Effort: "low"}},
		Claude: ClaudeSettings{EngineCommon: EngineCommon{Bin: "claude-dev", Model: "opus", Effort: "high"}},
	}}
	// Every "which engine's dial" question routes through the one selector.
	if got := c.Review.EngineCommon(c.Engine()); got.Bin != "claude-dev" || got.Model != "opus" || got.Effort != "high" {
		t.Errorf("EngineCommon(selected) = %+v, want the selected engine's block", *got)
	}
	if got := c.BinFor("codex"); got != "codex-dev" {
		t.Errorf("BinFor names an engine regardless of selection, got %q", got)
	}
}

// TestResolveBinDefaultsToTheEngineName covers the rule six call sites across
// five packages were each re-applying: an unset binary means the engine's own
// name. BinFor deliberately keeps reporting the unresolved value, because
// `config show` should say what is configured, not what would run.
func TestResolveBinDefaultsToTheEngineName(t *testing.T) {
	var bare Config
	if got := bare.ResolveBin("claude"); got != "claude" {
		t.Errorf("ResolveBin = %q, want the engine name", got)
	}
	if got := bare.BinFor("claude"); got != "" {
		t.Errorf("BinFor = %q, want the unresolved empty value", got)
	}
	set := Config{Review: ReviewSettings{Claude: ClaudeSettings{EngineCommon: EngineCommon{Bin: "/opt/claude"}}}}
	if got := set.ResolveBin("claude"); got != "/opt/claude" {
		t.Errorf("ResolveBin = %q, want the configured binary", got)
	}
	if got := DefaultBin("grok", ""); got != "grok" {
		t.Errorf("DefaultBin = %q: an engine nothing has configured still resolves to its name", got)
	}
}

func TestWithPolicyAppliesOnlyTheResolvedEnginesDials(t *testing.T) {
	base := ReviewSettings{
		Engine: "codex",
		Codex:  CodexSettings{EngineCommon: EngineCommon{Model: "gpt-5.6", Effort: "low"}},
		Claude: ClaudeSettings{EngineCommon: EngineCommon{Model: "sonnet", Effort: "low"}},
	}

	t.Run("switching engine moves the dials to that engine", func(t *testing.T) {
		got := base.WithPolicy(Policy{Engine: "claude", Model: "opus", Effort: "high"})
		if got.Engine != "claude" || got.Claude.Model != "opus" || got.Claude.Effort != "high" {
			t.Errorf("claude settings not applied: %+v", got.Claude)
		}
		if got.Codex.Model != "gpt-5.6" || got.Codex.Effort != "low" {
			t.Errorf("codex settings should be untouched, got %+v", got.Codex)
		}
	})

	t.Run("dials without an engine land on the configured one", func(t *testing.T) {
		got := base.WithPolicy(Policy{Model: "gpt-5.7"})
		if got.Engine != "codex" || got.Codex.Model != "gpt-5.7" {
			t.Errorf("codex model not applied: %+v", got)
		}
		if got.Claude.Model != "sonnet" {
			t.Errorf("claude settings should be untouched, got %+v", got.Claude)
		}
	})

	t.Run("an empty policy changes nothing", func(t *testing.T) {
		got := base.WithPolicy(Policy{})
		if got.Engine != base.Engine ||
			got.Codex.Model != base.Codex.Model || got.Codex.Effort != base.Codex.Effort ||
			got.Claude.Model != base.Claude.Model || got.Claude.Effort != base.Claude.Effort {
			t.Errorf("empty policy mutated settings: %+v", got)
		}
	})

	t.Run("the default engine is used when none is configured", func(t *testing.T) {
		got := ReviewSettings{}.WithPolicy(Policy{Model: "gpt-5.7"})
		if got.Codex.Model != "gpt-5.7" {
			t.Errorf("model should land on the default engine, got %+v", got)
		}
	})
}

func TestReachableEnginesAndTheirUsers(t *testing.T) {
	cfg := groupCfg()
	cfg.Review.Engine = "codex"

	engines := cfg.ReachableEngines()
	if len(engines) != 2 || engines[0] != "codex" {
		t.Fatalf("want [codex claude] with the default first, got %v", engines)
	}
	if !contains(engines, "claude") {
		t.Errorf("claude is reachable via group core, got %v", engines)
	}

	users := cfg.GroupsUsing("claude")
	if len(users) != 1 || users[0] != "group core" {
		t.Errorf("want claude attributed to group core, got %v", users)
	}
	if got := cfg.GroupsUsing("codex"); len(got) != 1 || got[0] != "(default)" {
		t.Errorf("want codex attributed to the default, got %v", got)
	}
}
