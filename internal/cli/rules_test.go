package cli

import (
	"strings"
	"testing"

	"github.com/shhac/lib-agent-cli/xdg"

	"github.com/shhac/agent-code-review/internal/config"
)

// runRulesCmd runs `prompts rules ...` (rules are nested under prompts).
func runRulesCmd(sub ...string) error {
	root := newRootCmd("test")
	root.SetArgs(append([]string{"prompts", "rules"}, sub...))
	return root.Execute()
}

// TestRulesAddLsRm drives the real commands against an isolated config dir:
// add two outcome-scoped rules, confirm they persist with the right condition
// fields, then remove one by name.
func TestRulesAddLsRm(t *testing.T) {
	cleanup := xdg.SetConfigBaseForTest(t.TempDir())
	defer cleanup()

	if err := runRulesCmd("add", "--name", "cmt-not-allowed",
		"--outcome", "comment", "--author-not-allowed", "--prompt", "NOT-ALLOWED"); err != nil {
		t.Fatal(err)
	}
	if err := runRulesCmd("add", "--name", "cmt-allowed",
		"--outcome", "comment", "--author-allowed", "--repo", "o/r", "--prompt", "ALLOWED"); err != nil {
		t.Fatal(err)
	}

	rules := config.Read().Review.Rules
	if len(rules) != 2 {
		t.Fatalf("want 2 rules, got %d: %+v", len(rules), rules)
	}
	if rules[0].When.Outcome != "comment" || !rules[0].When.AuthorNotAllowed || rules[0].Prompt != "NOT-ALLOWED" {
		t.Errorf("first rule not persisted correctly: %+v", rules[0])
	}
	if !rules[1].When.AuthorAllowed || len(rules[1].When.Repos) != 1 || rules[1].When.Repos[0] != "o/r" {
		t.Errorf("second rule not persisted correctly: %+v", rules[1])
	}

	// rm by name (case-insensitive) removes just the one.
	if err := runRulesCmd("rm", "CMT-NOT-ALLOWED"); err != nil {
		t.Fatal(err)
	}
	rules = config.Read().Review.Rules
	if len(rules) != 1 || rules[0].Name != "cmt-allowed" {
		t.Fatalf("rm left the wrong rules: %+v", rules)
	}
}

// TestRulesAddValidation pins the guard rails: required fields, mutually
// exclusive allow-list flags, bad enum values, duplicate names, rm of a
// missing rule.
func TestRulesAddValidation(t *testing.T) {
	cleanup := xdg.SetConfigBaseForTest(t.TempDir())
	defer cleanup()

	cases := []struct {
		name string
		args []string
	}{
		{"no name", []string{"add", "--prompt", "X"}},
		{"no prompt", []string{"add", "--name", "n"}},
		{"both allow flags", []string{"add", "--name", "n", "--prompt", "X", "--author-allowed", "--author-not-allowed"}},
		{"both gh-user flags", []string{"add", "--name", "n", "--prompt", "X", "--author-is-gh-user", "--author-not-gh-user"}},
		{"bad outcome", []string{"add", "--name", "n", "--prompt", "X", "--outcome", "merge"}},
		{"bad candidate-type", []string{"add", "--name", "n", "--prompt", "X", "--candidate-type", "ancient"}},
		{"bad repo", []string{"add", "--name", "n", "--prompt", "X", "--repo", "not-a-repo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := runRulesCmd(tc.args...); err == nil {
				t.Errorf("expected validation error for %s", tc.name)
			}
		})
	}
	// None of the invalid adds should have persisted anything.
	if got := len(config.Read().Review.Rules); got != 0 {
		t.Fatalf("invalid adds must not persist, got %d rules", got)
	}

	// A valid add, then a duplicate name must fail.
	if err := runRulesCmd("add", "--name", "dup", "--prompt", "X"); err != nil {
		t.Fatal(err)
	}
	if err := runRulesCmd("add", "--name", "DUP", "--prompt", "Y"); err == nil {
		t.Error("duplicate name (case-insensitive) must fail")
	}

	// rm of a missing rule must fail with a clear error.
	if err := runRulesCmd("rm", "nope"); err == nil || !strings.Contains(err.Error(), "No rule named") {
		t.Errorf("rm missing rule must fail clearly, got %v", err)
	}
}

// TestPromptsPreviewShapedCandidate drives `prompts preview` with candidate
// flags and confirms the command accepts the new axes and rejects bad values.
func TestPromptsPreviewShapedCandidate(t *testing.T) {
	cleanup := xdg.SetConfigBaseForTest(t.TempDir())
	defer cleanup()

	run := func(args ...string) error {
		root := newRootCmd("test")
		root.SetArgs(append([]string{"prompts", "preview"}, args...))
		return root.Execute()
	}

	if err := run("--candidate-type", "refreshed", "--repo", "o/r", "--author-is-gh-user", "--explain"); err != nil {
		t.Fatalf("shaped preview should succeed: %v", err)
	}
	if err := run("--candidate-type", "ancient"); err == nil {
		t.Error("invalid candidate-type must fail")
	}
	if err := run("--repo", "not-a-repo"); err == nil {
		t.Error("invalid repo must fail")
	}
}
