package review

import (
	"os"
	"path/filepath"
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

func TestBuildPromptMatchesRuleReposCaseInsensitively(t *testing.T) {
	cfg := config.Config{Review: config.ReviewSettings{
		MainPrompt: "MAIN",
		Rules: []config.Rule{
			{Name: "repo", When: config.Condition{Repos: []string{"Org/Repo"}}, Prompt: "REPO-ONLY"},
		},
	}}
	got := BuildPrompt(cfg, store.Candidate{Repo: "org/repo", Number: 7, Author: "bob"}, Facts{})
	if !strings.Contains(got, "REPO-ONLY") {
		t.Errorf("repo rule must match GitHub repo identity case-insensitively, got:\n%s", got)
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

// TestOutcomeScopedRules pins the headline feature: an allow-list-aware rule
// tagged with an outcome renders under that outcome's bullet, only in the
// matching variant, and never leaks into the prompt body.
func TestOutcomeScopedRules(t *testing.T) {
	cfg := config.Config{Review: config.ReviewSettings{
		MainPrompt: "MAIN",
		OnComment:  "COMMENT-BASE",
		Rules: []config.Rule{
			{Name: "cmt-not-allowed", When: config.Condition{Outcome: "comment", AuthorNotAllowed: true}, Prompt: "DENY-FRAG"},
			{Name: "cmt-allowed", When: config.Condition{Outcome: "comment", AuthorAllowed: true}, Prompt: "ALLOW-FRAG"},
		},
	}}
	c := store.Candidate{Repo: "o/r", Number: 7, Type: "new", Author: "alice"}

	// Not-allowed variant: base + not-allowed fragment, under the COMMENTED
	// bullet; the allowed fragment must not appear.
	got := BuildPrompt(cfg, c, Facts{AuthorAllowed: false})
	if !strings.Contains(got, "COMMENTED without approving: COMMENT-BASE DENY-FRAG") {
		t.Errorf("expected base + not-allowed fragment under the comment bullet, got:\n%s", got)
	}
	if strings.Contains(got, "ALLOW-FRAG") {
		t.Errorf("allowed fragment must not fire for a not-allowed author, got:\n%s", got)
	}

	// Allowed variant: base + allowed fragment; not-allowed fragment absent.
	got = BuildPrompt(cfg, c, Facts{AuthorAllowed: true})
	if !strings.Contains(got, "COMMENTED without approving: COMMENT-BASE ALLOW-FRAG") {
		t.Errorf("expected base + allowed fragment under the comment bullet, got:\n%s", got)
	}
	if strings.Contains(got, "DENY-FRAG") {
		t.Errorf("not-allowed fragment must not fire for an allowed author, got:\n%s", got)
	}

	// Outcome-scoped rules must never body-append: the only occurrence of the
	// fragment is inside the outcome section, not as a standalone trailing block.
	if strings.Count(got, "ALLOW-FRAG") != 1 {
		t.Errorf("outcome-scoped rule must render exactly once (under its bullet), got:\n%s", got)
	}
}

// TestOutcomeScopedRuleWithoutBaseSlot: an outcome bullet renders from a rule
// alone even when the base slot is empty.
func TestOutcomeScopedRuleWithoutBaseSlot(t *testing.T) {
	cfg := config.Config{Review: config.ReviewSettings{
		MainPrompt: "MAIN",
		Rules: []config.Rule{
			{Name: "rej", When: config.Condition{Outcome: "reject"}, Prompt: "REJECT-FRAG"},
		},
	}}
	got := BuildPrompt(cfg, store.Candidate{Repo: "o/r", Number: 1, Author: "bob"}, Facts{})
	if !strings.Contains(got, "REQUESTED CHANGES (rejected): REJECT-FRAG") {
		t.Errorf("pure-rule outcome bullet should render, got:\n%s", got)
	}
	// A comment/approve bullet with neither base nor rule stays omitted.
	if strings.Contains(got, "COMMENTED without approving") || strings.Contains(got, "APPROVED this PR") {
		t.Errorf("bullets with no content must be omitted, got:\n%s", got)
	}
}

// TestUntaggedRuleStillBodyAppends: a rule with no outcome keeps its original
// behaviour (appended to the prompt body, not routed to a bullet).
func TestUntaggedRuleStillBodyAppends(t *testing.T) {
	cfg := config.Config{Review: config.ReviewSettings{
		MainPrompt: "MAIN",
		OnComment:  "COMMENT-BASE",
		Rules: []config.Rule{
			{Name: "body", When: config.Condition{AuthorNotAllowed: true}, Prompt: "BODY-FRAG"},
		},
	}}
	got := BuildPrompt(cfg, store.Candidate{Repo: "o/r", Number: 1, Author: "carol"}, Facts{AuthorAllowed: false})
	if !strings.Contains(got, "BODY-FRAG") {
		t.Errorf("untagged rule must still fire, got:\n%s", got)
	}
	// It must not be pulled into the comment bullet.
	if strings.Contains(got, "COMMENTED without approving: COMMENT-BASE BODY-FRAG") {
		t.Errorf("untagged rule must not route to an outcome bullet, got:\n%s", got)
	}
}

// TestExplainRules pins the --explain trace: target routing (body vs outcome),
// match verdict, and a reason for the first failing condition.
func TestExplainRules(t *testing.T) {
	cfg := config.Config{Review: config.ReviewSettings{Rules: []config.Rule{
		{Name: "body-any", When: config.Condition{}, Prompt: "X"},
		{Name: "cmt-allowed", When: config.Condition{Outcome: "comment", AuthorAllowed: true}, Prompt: "X"},
		{Name: "repo-only", When: config.Condition{Repos: []string{"other/repo"}}, Prompt: "X"},
	}}}
	c := store.Candidate{Repo: "o/r", Type: "new", Author: "alice"}
	traces := ExplainRules(cfg, c, Facts{AuthorAllowed: true})

	if len(traces) != 3 {
		t.Fatalf("want 3 traces, got %d", len(traces))
	}
	if traces[0].Target != "body" || !traces[0].Matched {
		t.Errorf("wildcard rule should match under body: %+v", traces[0])
	}
	if traces[1].Target != "comment" || !traces[1].Matched {
		t.Errorf("allowed comment rule should match under comment: %+v", traces[1])
	}
	if traces[2].Matched || traces[2].Reason == "" {
		t.Errorf("repo-mismatch rule should be skipped with a reason: %+v", traces[2])
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
	// WORKING is the intermediate progress marker; a run that ends on it
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

// TestNewEngine pins the engine registry: empty defaults to codex, unknown
// names fail loudly at boot rather than mid-cycle.
func TestNewEngine(t *testing.T) {
	for _, name := range []string{"", "codex"} {
		e, err := NewEngine(config.ReviewSettings{Engine: name})
		if _, ok := e.(*codexEngine); err != nil || !ok {
			t.Errorf("NewEngine(%q) = %v, %v; want the codex engine", name, e, err)
		}
	}
	if _, err := NewEngine(config.ReviewSettings{Engine: "mystery"}); err == nil {
		t.Error("unknown engine must fail")
	}
}

// TestMainPrompt pins the path-wins-over-inline resolution, including the
// unreadable-path fallback.
func TestMainPrompt(t *testing.T) {
	if got := MainPrompt(config.ReviewSettings{MainPrompt: "  inline  "}); got != "inline" {
		t.Errorf("inline prompt = %q", got)
	}
	p := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(p, []byte("from file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := MainPrompt(config.ReviewSettings{MainPrompt: "inline", MainPromptPath: p}); got != "from file" {
		t.Errorf("path must win, got %q", got)
	}
	missing := filepath.Join(t.TempDir(), "absent.md")
	if got := MainPrompt(config.ReviewSettings{MainPrompt: "inline", MainPromptPath: missing}); got != "inline" {
		t.Errorf("unreadable path must fall back to inline, got %q", got)
	}
}
