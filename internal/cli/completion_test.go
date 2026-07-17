package cli

import (
	"context"
	"reflect"
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
)

func TestConfigCompletionHooks(t *testing.T) {
	root := newRootCmd("test")
	configCmd := findCommand(root, "config")
	for _, verb := range []string{"get", "set", "unset"} {
		if findCommand(configCmd, verb).ValidArgsFunction == nil {
			t.Errorf("config %s must have argument completion", verb)
		}
	}
	set := findCommand(configCmd, "set")
	keys, _ := set.ValidArgsFunction(set, nil, "codex.")
	if !reflect.DeepEqual(keys, []string{"codex.bin", "codex.effort", "codex.max_resumes", "codex.model", "codex.sandbox"}) {
		t.Errorf("codex config key completion = %v", keys)
	}
	for _, tc := range []struct {
		key, prefix string
		want        []string
	}{
		{"schedule.enabled", "", []string{"false", "true"}},
		{"review.engine", "", []string{"codex"}},
		{"codex.sandbox", "workspace", []string{"workspace-write"}},
		{"dashboard.tailscale.mode", "", []string{"funnel", "serve"}},
	} {
		if got := completeConfigValue(context.Background(), tc.key, tc.prefix); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s completion = %v, want %v", tc.key, got, tc.want)
		}
	}
}

func TestPromptAndRepoCompletions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := config.Write(config.Config{Repos: []string{"z/repo", "alpha/web"}}); err != nil {
		t.Fatal(err)
	}
	set := promptsSetCmd()
	if got, _ := set.ValidArgsFunction(set, nil, "on-"); !reflect.DeepEqual(got, []string{"on-approve", "on-comment", "on-reject"}) {
		t.Errorf("prompt slot completion = %v", got)
	}
	rm := reposRmCmd()
	if got, _ := rm.ValidArgsFunction(rm, nil, ""); !reflect.DeepEqual(got, []string{"alpha/web", "z/repo"}) {
		t.Errorf("repo completion = %v", got)
	}
}

func TestParseCodexModels(t *testing.T) {
	models, err := parseCodexModels([]byte(`{"models":[{"slug":"gpt-5.6-terra","supported_reasoning_levels":[{"effort":"low"},{"effort":"ultra"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := models[0].Slug; got != "gpt-5.6-terra" {
		t.Errorf("slug = %q", got)
	}
	if got := models[0].SupportedReasoningLevels[1].Effort; got != "ultra" {
		t.Errorf("effort = %q", got)
	}
}
