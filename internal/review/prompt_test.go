package review

import (
	"strings"
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
)

func TestDeriveFacts(t *testing.T) {
	cases := []struct {
		name           string
		author, ghUser string
		approvable     bool
		wantIsGH       bool
	}{
		{"self-review", "bob", "bob", false, true},
		{"approvable", "alice", "bob", true, false},
		{"stranger", "carol", "bob", false, false},
		{"no gh user", "bob", "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := DeriveFacts(store.Candidate{Author: tc.author}, tc.ghUser, tc.approvable)
			if f.AuthorIsGHUser != tc.wantIsGH {
				t.Errorf("AuthorIsGHUser = %v, want %v", f.AuthorIsGHUser, tc.wantIsGH)
			}
			if f.AuthorApprovable != tc.approvable {
				t.Errorf("AuthorApprovable = %v, want %v", f.AuthorApprovable, tc.approvable)
			}
		})
	}
}

func TestBuildPromptAppendsMatchingRules(t *testing.T) {
	cfg := config.Config{
		Review: config.ReviewSettings{
			MainPrompt: "MAIN",
			Rules: []config.Rule{
				{Name: "self", When: config.Condition{AuthorIsGHUser: true}, Prompt: "SELF-ONLY"},
				{Name: "stranger", When: config.Condition{AuthorNotInAllowlist: true}, Prompt: "STRANGER-ONLY"},
				{Name: "refreshed", When: config.Condition{CandidateType: "refreshed"}, Prompt: "REFRESHED-ONLY"},
			},
		},
	}
	c := store.Candidate{Repo: "o/r", Number: 7, Type: "new", Author: "bob"}

	// Self-review: only the self rule fires.
	got := BuildPrompt(cfg, c, Facts{AuthorIsGHUser: true, AuthorApprovable: false})
	if !strings.Contains(got, "MAIN") || !strings.Contains(got, "SELF-ONLY") {
		t.Errorf("expected MAIN and SELF-ONLY, got:\n%s", got)
	}
	if strings.Contains(got, "REFRESHED-ONLY") {
		t.Errorf("refreshed rule should not fire for a new PR")
	}

	// Approvable author on a new PR: no stranger, no self, no refreshed rule.
	got = BuildPrompt(cfg, c, Facts{AuthorApprovable: true})
	if strings.Contains(got, "SELF-ONLY") || strings.Contains(got, "STRANGER-ONLY") {
		t.Errorf("no author rule should fire for allowlisted author, got:\n%s", got)
	}
}

func TestParseDecision(t *testing.T) {
	if got := parseDecision("… the review APPROVEs this change"); got != DecisionApprove {
		t.Errorf("expected APPROVE, got %s", got)
	}
	if got := parseDecision("I will DO NOT APPROVE, leaving comments"); got != DecisionComment {
		t.Errorf("expected COMMENT for 'do not approve', got %s", got)
	}
	if got := parseDecision("left some comments"); got != DecisionComment {
		t.Errorf("expected COMMENT default, got %s", got)
	}
}
