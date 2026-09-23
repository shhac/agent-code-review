package cli

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"reflect"
	"testing"

	"github.com/spf13/cobra"

	"github.com/shhac/crew-code-review/internal/config"
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
	if !reflect.DeepEqual(keys, []string{"codex.bin", "codex.effort", "codex.max_resumes", "codex.model",
		"codex.sandbox", "codex.usage_floor", "codex.usage_floor.1w_percent", "codex.usage_floor.5h_percent"}) {
		t.Errorf("codex config key completion = %v", keys)
	}
	for _, tc := range []struct {
		key, prefix string
		want        []string
	}{
		{"schedule.enabled", "", []string{"false", "true"}},
		{"review.engine", "", []string{"claude", "codex"}}, // completion sorts; review.Engines is default-first
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

// Completion runs on every tab press, possibly while the daemon holds the
// store, so it must never write. The score commands' --author completion used
// to open read-WRITE: every tab press applied the schema (creating a database
// on a machine that had none) and contended with the daemon for the lock.
func TestScoreAuthorCompletionNeverWritesTheStore(t *testing.T) {
	if _, err := exec.LookPath("duckdb"); err != nil {
		t.Skip("duckdb CLI not on PATH")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for _, cmd := range []*cobra.Command{scoreLsCmd(), scoreRecomputeCmd(), scoreRefetchCmd()} {
		complete, ok := cmd.GetFlagCompletionFunc("author")
		if !ok {
			t.Fatalf("%s --author has no completion", cmd.Name())
		}
		cmd.SetContext(context.Background())
		complete(cmd, nil, "")
	}
	if path := (config.Config{}).StorePath(); fileExists(path) {
		t.Errorf("author completion created the store at %s", path)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, fs.ErrNotExist)
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
