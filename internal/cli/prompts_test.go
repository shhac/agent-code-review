package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shhac/lib-agent-cli/xdg"

	"github.com/shhac/agent-code-review/internal/config"
)

func runPromptsCmd(sub ...string) error {
	root := newRootCmd("test")
	root.SetArgs(append([]string{"prompts"}, sub...))
	return root.Execute()
}

// TestSlotField pins the slot→field mapping behind every prompts command: a
// wrong case arm would silently write the review agent's instructions into
// the wrong slot (including on-approve, which feeds the approval flow).
func TestSlotField(t *testing.T) {
	r := config.ReviewSettings{
		MainPrompt:   "MAIN",
		OnApprove:    "APPROVE",
		OnComment:    "COMMENT",
		OnReject:     "REJECT",
		ResumePrompt: "RESUME",
	}
	cases := map[string]string{
		"main":       "MAIN",
		"on-approve": "APPROVE",
		"on-comment": "COMMENT",
		"on-reject":  "REJECT",
		"resume":     "RESUME",
	}
	if len(cases) != len(promptSlots) {
		t.Fatalf("test covers %d slots, promptSlots has %d: keep them in step", len(cases), len(promptSlots))
	}
	for slot, want := range cases {
		p := slotField(&r, slot)
		if p == nil {
			t.Fatalf("slotField(%q) = nil", slot)
		}
		if *p != want {
			t.Errorf("slotField(%q) reads %q, want %q: slot mapped to the wrong field", slot, *p, want)
		}
	}
	if slotField(&r, "nope") != nil {
		t.Error("unknown slot must map to nil")
	}
}

// TestPromptsSetUnset drives the real commands against an isolated config
// dir: set persists into the right config leaf, unknown slots error, and
// unset clears.
func TestPromptsSetUnset(t *testing.T) {
	cleanup := xdg.SetConfigBaseForTest(t.TempDir())
	defer cleanup()

	if err := runPromptsCmd("set", "on-approve", "notify the channel"); err != nil {
		t.Fatal(err)
	}
	if got := config.Read().Review.OnApprove; got != "notify the channel" {
		t.Errorf("on_approve = %q, want the set text", got)
	}
	if config.Read().Review.MainPrompt != "" {
		t.Error("setting on-approve must not touch other slots")
	}

	if err := runPromptsCmd("set", "bogus", "text"); err == nil {
		t.Error("unknown slot must error")
	}

	if err := runPromptsCmd("unset", "on-approve"); err != nil {
		t.Fatal(err)
	}
	if got := config.Read().Review.OnApprove; got != "" {
		t.Errorf("on_approve after unset = %q, want empty", got)
	}
}

// TestPromptsSetMainRefusesPathOverride pins the guard: with
// main_prompt_path set, `prompts set main` must refuse (the file would
// silently win over the new inline text) and leave the config unchanged.
func TestPromptsSetMainRefusesPathOverride(t *testing.T) {
	cleanup := xdg.SetConfigBaseForTest(t.TempDir())
	defer cleanup()

	promptFile := filepath.Join(t.TempDir(), "main.md")
	if err := os.WriteFile(promptFile, []byte("FILE PROMPT"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.Update(func(cfg *config.Config) error {
		cfg.Review.MainPromptPath = promptFile
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := runPromptsCmd("set", "main", "inline text"); err == nil {
		t.Fatal("set main must refuse while main_prompt_path overrides it")
	}
	cfg := config.Read()
	if cfg.Review.MainPrompt != "" {
		t.Errorf("refused set must not persist, got main_prompt = %q", cfg.Review.MainPrompt)
	}
	if cfg.Review.MainPromptPath != promptFile {
		t.Errorf("main_prompt_path must be untouched, got %q", cfg.Review.MainPromptPath)
	}
}
