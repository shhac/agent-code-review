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

func TestApprovalDirectiveDefaultsToCommentOnly(t *testing.T) {
	cfg := config.Config{Review: config.ReviewSettings{MainPrompt: "MAIN"}}
	c := store.Candidate{Repo: "o/r", Number: 5, Author: "carol"}

	// Not approvable → hard "do not approve", no reason leaked.
	got := BuildPrompt(cfg, c, Facts{AuthorApprovable: false})
	if !strings.Contains(got, "DO NOT approve") {
		t.Errorf("expected a hard do-not-approve directive, got:\n%s", got)
	}

	// Self-review, even if approvable → still comment-only, and must not reveal
	// that it's self-authored (would leak the gh user).
	got = BuildPrompt(cfg, c, Facts{AuthorApprovable: true, AuthorIsGHUser: true})
	if !strings.Contains(got, "DO NOT approve") {
		t.Errorf("self-review must be comment-only even when approvable, got:\n%s", got)
	}
	if strings.Contains(got, "self") || strings.Contains(got, "your own") {
		t.Errorf("directive must not reveal self-authorship, got:\n%s", got)
	}

	// Approvable and not self → approval permitted.
	got = BuildPrompt(cfg, c, Facts{AuthorApprovable: true})
	if strings.Contains(got, "DO NOT approve") || !strings.Contains(got, "MAY approve") {
		t.Errorf("approvable author should be allowed to be approved, got:\n%s", got)
	}
}

func TestParseVerdict(t *testing.T) {
	v, err := parseVerdict([]byte(`{"decision":"APPROVED","summary":"looks good, approved on GitHub"}`))
	if err != nil || v.Decision != DecisionApproved || v.Summary == "" {
		t.Errorf("expected APPROVED verdict, got %+v err=%v", v, err)
	}
	if _, err := parseVerdict([]byte(`{"decision":"MAYBE","summary":"?"}`)); err == nil {
		t.Error("invalid decision must be rejected")
	}
	if _, err := parseVerdict([]byte(``)); err == nil {
		t.Error("empty report must be rejected")
	}
	if _, err := parseVerdict([]byte(`not json`)); err == nil {
		t.Error("non-JSON report must be rejected")
	}
	// ERROR is the driver's value, never a valid agent report.
	if _, err := parseVerdict([]byte(`{"decision":"ERROR","summary":"x"}`)); err == nil {
		t.Error("agent must not be able to report ERROR")
	}
}
