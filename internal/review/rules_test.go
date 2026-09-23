package review

import (
	"strings"
	"testing"

	"github.com/shhac/crew-code-review/internal/config"
	"github.com/shhac/crew-code-review/internal/store"
)

// TestExplainRules pins the --explain trace: target routing (body vs outcome),
// match verdict, and a reason for the first failing condition.
func TestExplainRules(t *testing.T) {
	cfg := config.Config{Review: config.ReviewSettings{Rules: []config.Rule{
		{Name: "body-any", When: config.Condition{}, Prompt: "X"},
		{Name: "cmt-allowed", When: config.Condition{Outcome: "comment", AuthorAllowed: true}, Prompt: "X"},
		{Name: "repo-only", When: config.Condition{Repos: []string{"other/repo"}}, Prompt: "X"},
	}}}
	c := store.Candidate{Repo: "o/r", Type: "new", Author: "alice"}
	traces := ExplainRules(cfg, c, Facts{Policy: approvable()})

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

// TestAuthorNotGHUserCondition pins the negation twin: author_not_gh_user
// excludes self-authored PRs, making the self / not-self split mutually
// exclusive against author_is_gh_user.
func TestAuthorNotGHUserCondition(t *testing.T) {
	notSelf := config.Condition{AuthorNotGHUser: true}
	self := config.Condition{AuthorIsGHUser: true}
	c := store.Candidate{Repo: "o/r", Type: "new", Author: "bob"}

	// Self-authored: not-self rule skips, self rule matches.
	if matches(notSelf, c, Facts{AuthorIsGHUser: true}) {
		t.Error("author_not_gh_user must NOT match a self-authored PR")
	}
	if !matches(self, c, Facts{AuthorIsGHUser: true}) {
		t.Error("author_is_gh_user must match a self-authored PR")
	}
	// Someone else's PR: the reverse.
	if !matches(notSelf, c, Facts{AuthorIsGHUser: false}) {
		t.Error("author_not_gh_user must match a non-self PR")
	}
	if matches(self, c, Facts{AuthorIsGHUser: false}) {
		t.Error("author_is_gh_user must NOT match a non-self PR")
	}
}

// Rules can gate on the resolved group and on the handle itself, which is how
// a cohort gets conditional instructions the group's own flat prompt cannot
// express (outcome-scoped, type-scoped, repo-scoped).
func TestRulesMatchGroupsAndAuthors(t *testing.T) {
	cfg := config.Config{Review: config.ReviewSettings{
		MainPrompt: "MAIN",
		Rules: []config.Rule{
			{Name: "contractors", When: config.Condition{Groups: []string{"contractor", "intern"}}, Prompt: "CONTRACTOR-FRAG"},
			{Name: "named", When: config.Condition{Authors: []string{"Alice"}}, Prompt: "ALICE-FRAG"},
		},
	}}
	c := store.Candidate{Repo: "org/repo", Number: 7, Author: "alice"}

	inGroup := config.Policy{Group: "contractor", Review: config.ReviewComment}
	got := BuildPrompt(cfg, c, Facts{Policy: inGroup})
	if !strings.Contains(got, "CONTRACTOR-FRAG") {
		t.Errorf("a groups condition must fire for a member, got:\n%s", got)
	}
	// Handles are GitHub's, so they match the way GitHub treats them.
	if !strings.Contains(got, "ALICE-FRAG") {
		t.Errorf("an authors condition must match case-insensitively, got:\n%s", got)
	}

	outOfGroup := BuildPrompt(cfg, store.Candidate{Repo: "org/repo", Number: 8, Author: "bob"}, Facts{Policy: approvable()})
	if strings.Contains(outOfGroup, "CONTRACTOR-FRAG") || strings.Contains(outOfGroup, "ALICE-FRAG") {
		t.Errorf("neither rule may fire for a different author in a different group, got:\n%s", outOfGroup)
	}

	// The skip reason names the condition that failed, so --explain is useful.
	traces := ExplainRules(cfg, store.Candidate{Author: "bob"}, Facts{Policy: approvable()})
	for _, tr := range traces {
		if tr.Matched {
			t.Errorf("no rule should match, got %+v", tr)
		}
		if tr.Reason == "" {
			t.Errorf("a skipped rule must say why, got %+v", tr)
		}
	}
}

// author_allowed predates groups. It survives as an alias for "the resolved
// policy permits approval", so rules written before groups keep their meaning.
func TestAuthorAllowedConditionAliasesTheApproveLevel(t *testing.T) {
	cfg := config.Config{Review: config.ReviewSettings{
		MainPrompt: "MAIN",
		Rules: []config.Rule{
			{Name: "allowed", When: config.Condition{AuthorAllowed: true}, Prompt: "ALLOWED-FRAG"},
			{Name: "not-allowed", When: config.Condition{AuthorNotAllowed: true}, Prompt: "STRANGER-FRAG"},
		},
	}}
	c := store.Candidate{Repo: "org/repo", Number: 7, Author: "alice"}

	// A group whose level is approve satisfies author_allowed, whatever it is
	// called; a comment-level group satisfies author_not_allowed.
	if got := BuildPrompt(cfg, c, Facts{Policy: config.Policy{Group: "anything", Review: config.ReviewApprove}}); !strings.Contains(got, "ALLOWED-FRAG") || strings.Contains(got, "STRANGER-FRAG") {
		t.Errorf("approve level must satisfy author_allowed only, got:\n%s", got)
	}
	if got := BuildPrompt(cfg, c, Facts{Policy: config.Policy{Group: "anything", Review: config.ReviewComment}}); !strings.Contains(got, "STRANGER-FRAG") || strings.Contains(got, "ALLOWED-FRAG") {
		t.Errorf("comment level must satisfy author_not_allowed only, got:\n%s", got)
	}
}
