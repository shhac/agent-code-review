package cli

import (
	"strings"
	"testing"

	"github.com/shhac/lib-agent-cli/xdg"

	"github.com/shhac/agent-code-review/internal/config"
)

func runRulesCmd(args ...string) error {
	root := newRootCmd("test")
	root.SetArgs(args)
	return root.Execute()
}

// TestRulesAddLsRm drives the real commands against an isolated config dir:
// add two outcome-scoped rules, confirm they persist with the right condition
// fields, then remove one by name.
func TestRulesAddLsRm(t *testing.T) {
	cleanup := xdg.SetConfigBaseForTest(t.TempDir())
	defer cleanup()

	if err := runRulesCmd("rules", "add", "--name", "cmt-not-allowed",
		"--outcome", "comment", "--author-not-allowed", "--prompt", "NOT-ALLOWED"); err != nil {
		t.Fatal(err)
	}
	if err := runRulesCmd("rules", "add", "--name", "cmt-allowed",
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
	if err := runRulesCmd("rules", "rm", "CMT-NOT-ALLOWED"); err != nil {
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
		{"no name", []string{"rules", "add", "--prompt", "X"}},
		{"no prompt", []string{"rules", "add", "--name", "n"}},
		{"both allow flags", []string{"rules", "add", "--name", "n", "--prompt", "X", "--author-allowed", "--author-not-allowed"}},
		{"bad outcome", []string{"rules", "add", "--name", "n", "--prompt", "X", "--outcome", "merge"}},
		{"bad candidate-type", []string{"rules", "add", "--name", "n", "--prompt", "X", "--candidate-type", "ancient"}},
		{"bad repo", []string{"rules", "add", "--name", "n", "--prompt", "X", "--repo", "not-a-repo"}},
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
	if err := runRulesCmd("rules", "add", "--name", "dup", "--prompt", "X"); err != nil {
		t.Fatal(err)
	}
	if err := runRulesCmd("rules", "add", "--name", "DUP", "--prompt", "Y"); err == nil {
		t.Error("duplicate name (case-insensitive) must fail")
	}

	// rm of a missing rule must fail with a clear error.
	if err := runRulesCmd("rules", "rm", "nope"); err == nil || !strings.Contains(err.Error(), "No rule named") {
		t.Errorf("rm missing rule must fail clearly, got %v", err)
	}
}
