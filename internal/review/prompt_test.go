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
		allowed        bool
		wantIsGH       bool
	}{
		{"self-review", "bob", "bob", false, true},
		{"allowed author", "alice", "bob", true, false},
		{"stranger", "carol", "bob", false, false},
		{"no gh user", "bob", "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := DeriveFacts(store.Candidate{Author: tc.author}, tc.ghUser, tc.allowed)
			if f.AuthorIsGHUser != tc.wantIsGH {
				t.Errorf("AuthorIsGHUser = %v, want %v", f.AuthorIsGHUser, tc.wantIsGH)
			}
			if f.AuthorAllowed != tc.allowed {
				t.Errorf("AuthorAllowed = %v, want %v", f.AuthorAllowed, tc.allowed)
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
				{Name: "stranger", When: config.Condition{AuthorNotAllowed: true}, Prompt: "STRANGER-ONLY"},
				{Name: "refreshed", When: config.Condition{CandidateType: "refreshed"}, Prompt: "REFRESHED-ONLY"},
			},
		},
	}
	c := store.Candidate{Repo: "o/r", Number: 7, Type: "new", Author: "bob"}

	// Self-review: only the self rule fires.
	got := BuildPrompt(cfg, c, Facts{AuthorIsGHUser: true, AuthorAllowed: false})
	if !strings.Contains(got, "MAIN") || !strings.Contains(got, "SELF-ONLY") {
		t.Errorf("expected MAIN and SELF-ONLY, got:\n%s", got)
	}
	if strings.Contains(got, "REFRESHED-ONLY") {
		t.Errorf("refreshed rule should not fire for a new PR")
	}

	// Allowed author on a new PR: no stranger, no self, no refreshed rule.
	got = BuildPrompt(cfg, c, Facts{AuthorAllowed: true})
	if strings.Contains(got, "SELF-ONLY") || strings.Contains(got, "STRANGER-ONLY") {
		t.Errorf("no author rule should fire for allowed author, got:\n%s", got)
	}
}

func TestApprovalDirectiveDefaultsToCommentOnly(t *testing.T) {
	cfg := config.Config{Review: config.ReviewSettings{MainPrompt: "MAIN"}}
	c := store.Candidate{Repo: "o/r", Number: 5, Author: "carol"}

	// Author not allowed → hard "do not approve", no reason leaked.
	got := BuildPrompt(cfg, c, Facts{AuthorAllowed: false})
	if !strings.Contains(got, "DO NOT approve") {
		t.Errorf("expected a hard do-not-approve directive, got:\n%s", got)
	}

	// Self-review, even for an allowed author → still comment-only, and must not reveal
	// that it's self-authored (would leak the gh user).
	got = BuildPrompt(cfg, c, Facts{AuthorAllowed: true, AuthorIsGHUser: true})
	if !strings.Contains(got, "DO NOT approve") {
		t.Errorf("self-review must be comment-only even for an allowed author, got:\n%s", got)
	}
	if strings.Contains(got, "self") || strings.Contains(got, "your own") {
		t.Errorf("directive must not reveal self-authorship, got:\n%s", got)
	}

	// Allowed author and not self → approval permitted.
	got = BuildPrompt(cfg, c, Facts{AuthorAllowed: true})
	if strings.Contains(got, "DO NOT approve") || !strings.Contains(got, "MAY approve") {
		t.Errorf("allowed author should be approvable, got:\n%s", got)
	}
}

func TestOutcomeInstructions(t *testing.T) {
	cfg := config.Config{Review: config.ReviewSettings{
		MainPrompt: "MAIN",
		OnApprove:  "notify per team convention",
		OnReject:   "explain what blocks it",
	}}
	c := store.Candidate{Repo: "o/r", Number: 9, Author: "alice"}

	got := BuildPrompt(cfg, c, Facts{AuthorAllowed: true})
	if !strings.Contains(got, "If you APPROVED this PR: notify per team convention") {
		t.Errorf("missing on_approve instruction, got:\n%s", got)
	}
	if !strings.Contains(got, "If you REQUESTED CHANGES (rejected): explain what blocks it") {
		t.Errorf("missing on_reject instruction, got:\n%s", got)
	}
	if strings.Contains(got, "COMMENTED without approving") {
		t.Errorf("unset on_comment must not appear, got:\n%s", got)
	}

	// No outcomes configured → whole section omitted.
	got = BuildPrompt(config.Config{Review: config.ReviewSettings{MainPrompt: "MAIN"}}, c, Facts{})
	if strings.Contains(got, "matching your outcome") {
		t.Errorf("outcome section must be omitted when nothing is configured, got:\n%s", got)
	}
}

func TestParseVerdict(t *testing.T) {
	v, err := parseVerdict([]byte(`{"decision":"APPROVED","summary":"looks good, approved on GitHub"}`))
	if err != nil || v.Decision != DecisionApproved || v.Summary == "" {
		t.Errorf("expected APPROVED verdict, got %+v err=%v", v, err)
	}
	if v, err := parseVerdict([]byte(`{"decision":"REQUESTED_CHANGES","summary":"blocked on migration"}`)); err != nil || v.Decision != DecisionRequestedChanges {
		t.Errorf("REQUESTED_CHANGES must be a valid report, got %+v err=%v", v, err)
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
	// WORKING is the intermediate progress marker — a run that ends on it
	// was cut short and must not record an outcome.
	if _, err := parseVerdict([]byte(`{"decision":"WORKING","summary":"still reading the diff"}`)); err == nil {
		t.Error("a final WORKING report must be rejected")
	}
}

// TestParseTokensUsed pins the "tokens used" trailer extraction from the
// engine transcript, including the comma-grouped count and the repeated
// final message that follows it.
func TestParseTokensUsed(t *testing.T) {
	raw := "codex\n{\"decision\":\"APPROVED\",\"summary\":\"done\"}\ntokens used\n192,575\n{\"decision\":\"APPROVED\",\"summary\":\"done\"}"
	if got := parseTokensUsed(raw); got != 192575 {
		t.Errorf("tokens = %d, want 192575", got)
	}
	if got := parseTokensUsed("no trailer here"); got != 0 {
		t.Errorf("missing trailer must yield 0, got %d", got)
	}
	if got := parseTokensUsed("tokens used\n941"); got != 941 {
		t.Errorf("ungrouped count must parse, got %d", got)
	}
}
