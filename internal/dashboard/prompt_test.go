package dashboard

import (
	"net/http"
	"strings"
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
)

// promptServer builds a config-only Server (the prompt handlers never touch the
// store) with the given review settings and watched repos.
func promptServer(review config.ReviewSettings, repos ...string) *Server {
	return &Server{config: func() config.Config {
		return config.Config{Repos: repos, Review: review}
	}}
}

// TestHandlePrompt pins the read-only /api/prompt response shape: the slots,
// the main-prompt resolution, and the watched repos surfaced for the picker.
func TestHandlePrompt(t *testing.T) {
	s := promptServer(config.ReviewSettings{
		MainPrompt: "MAIN",
		OnApprove:  "approve-text",
		OnComment:  "comment-text",
	}, "o/two", "o/one")

	code, resp := serveJSON[promptResp](t, s.handlePrompt, http.MethodGet, "/api/prompt", "")
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	if resp.MainPrompt != "MAIN" {
		t.Errorf("main_prompt = %q", resp.MainPrompt)
	}
	if resp.Outcomes.OnApprove != "approve-text" || resp.Outcomes.OnComment != "comment-text" || resp.Outcomes.OnReject != "" {
		t.Errorf("outcomes not surfaced: %+v", resp.Outcomes)
	}
	// Repos are sorted for the picker.
	if len(resp.Repos) != 2 || resp.Repos[0] != "o/one" || resp.Repos[1] != "o/two" {
		t.Errorf("repos = %v, want sorted [o/one o/two]", resp.Repos)
	}
}

// TestHandlePromptPreviewValidation pins the 400 branches and their bodies.
func TestHandlePromptPreviewValidation(t *testing.T) {
	s := promptServer(config.ReviewSettings{MainPrompt: "MAIN"})
	cases := []struct {
		name, query, wantErr string
	}{
		{"bad candidate_type", "candidate_type=ancient", "candidate_type must be new or refreshed"},
		{"bad repo", "repo=not-a-repo", "repo must be owner/name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, resp := serveJSON[map[string]string](t, s.handlePromptPreview, http.MethodGet, "/api/prompt/preview?"+tc.query, "")
			if code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400", code)
			}
			if resp["error"] != tc.wantErr {
				t.Errorf("error = %q, want %q", resp["error"], tc.wantErr)
			}
		})
	}
}

// TestHandlePromptPreviewDefaults pins the defaulting (empty repo/type) and the
// 200 response shape the dashboard consumes.
func TestHandlePromptPreviewDefaults(t *testing.T) {
	s := promptServer(config.ReviewSettings{
		MainPrompt: "MAIN",
		OnComment:  "COMMENT-BASE",
		Rules: []config.Rule{
			{Name: "cmt", When: config.Condition{Outcome: "comment"}, Prompt: "RULE-FRAG"},
		},
	})
	code, resp := serveJSON[promptPreviewResp](t, s.handlePromptPreview, http.MethodGet, "/api/prompt/preview", "")
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	if resp.Candidate.Repo != "example-org/example-repo" || resp.Candidate.CandidateType != "new" {
		t.Errorf("defaults not applied: %+v", resp.Candidate)
	}
	if !strings.Contains(resp.Preview, "MAIN") || !strings.Contains(resp.Preview, "COMMENT-BASE") {
		t.Errorf("preview missing assembled content:\n%s", resp.Preview)
	}
	if len(resp.Rules) != 1 || !resp.Rules[0].Matched {
		t.Errorf("rule trace not populated: %+v", resp.Rules)
	}
}

// TestHandlePromptPreviewApprovalPolicy pins the security-relevant, asymmetric
// boolean parsing: author_allowed flips only on exact "false";
// author_is_gh_user defaults false and needs exact "true". The previewed
// approval policy (MAY vs DO NOT approve) must follow.
//
// With NO parameters the answer is now an author who has no roster row, which
// resolves through authors.unlisted rather than assuming approval. The old
// default assumed the approver group, so the emptiest possible query claimed a
// random contributor could be approved: the wrong direction to be wrong in.
func TestHandlePromptPreviewApprovalPolicy(t *testing.T) {
	s := promptServer(config.ReviewSettings{MainPrompt: "MAIN"})
	policyFor := func(query string) string {
		_, resp := serveJSON[promptPreviewResp](t, s.handlePromptPreview, http.MethodGet, "/api/prompt/preview?"+query, "")
		if strings.Contains(resp.Preview, "DO NOT approve") {
			return "deny"
		}
		if strings.Contains(resp.Preview, "MAY approve") {
			return "allow"
		}
		return "?"
	}
	cases := []struct {
		query, want string
	}{
		{"", "deny"},                                            // nobody named: unrostered, so no approval
		{"author_allowed=true", "allow"},                        // the legacy flag still works when passed
		{"author_allowed=false", "deny"},                        // exact "false" flips it
		{"author_allowed=False", "allow"},                       // only exact lowercase "false" flips
		{"author_allowed=0", "allow"},                           // "0" is not "false"
		{"author_is_gh_user=true", "deny"},                      // self-review is always comment-only
		{"author_allowed=true&author_is_gh_user=True", "allow"}, // strict: "True" != "true"
	}
	for _, tc := range cases {
		if got := policyFor(tc.query); got != tc.want {
			t.Errorf("policy for %q = %q, want %q", tc.query, got, tc.want)
		}
	}
}

// The preview pickers need the DEFINED groups, not the ones that happen to
// have members: a cohort with nobody in it yet is exactly the one you preview
// while writing its prompt. Deriving them from the roster would miss those and
// the built-ins.
func TestPromptGroupsListsDefinedAndBuiltinCohorts(t *testing.T) {
	got := promptGroups(config.Config{
		Authors: config.AuthorSettings{
			Groups: map[string]config.Group{
				"contractors": {Review: config.ReviewComment},
				"nobody-yet":  {Review: config.ReviewIgnore},
				"no-level":    {}, // an omitted review level means comment
			},
		},
	})

	byName := map[string]promptGroup{}
	for _, g := range got {
		byName[g.Name] = g
	}
	for _, name := range []string{"contractors", "nobody-yet", "no-level", "approver", "commenter", "ignored"} {
		if _, ok := byName[name]; !ok {
			t.Errorf("group %q missing from the picker list: %+v", name, got)
		}
	}
	if g := byName["nobody-yet"]; g.Review != config.ReviewIgnore || g.Builtin {
		t.Errorf("a declared group with no members must still be offered: %+v", g)
	}
	if g := byName["no-level"]; g.Review != config.ReviewComment {
		t.Errorf("a group with no review level resolves to comment, got %q", g.Review)
	}
	if g := byName["approver"]; !g.Builtin {
		t.Errorf("approver must be marked built-in: %+v", g)
	}
}

// The two pickers compose, and the group wins: picking an author AND a group
// answers "what would they get if I moved them", which is the point of being
// able to set both.
func TestPromptPreviewGroupOverridesTheRosterLookup(t *testing.T) {
	cfg := config.Config{
		Review: config.ReviewSettings{MainPrompt: "MAIN"},
		Authors: config.AuthorSettings{
			Groups: map[string]config.Group{
				"contractors": {Review: config.ReviewComment, Prompt: "CONTRACTOR-FRAGMENT"},
			},
			Overrides: []config.AuthorOverride{{
				Handle: "alice",
				Group:  config.Group{Prompt: "ALICE-FRAGMENT"},
			}},
		},
	}
	s := &Server{config: func() config.Config { return cfg }}

	code, resp := serveJSON[promptPreviewResp](t, s.handlePromptPreview, http.MethodGet,
		"/api/prompt/preview?author=alice&group=contractors&repo=org/repo", "")
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	if resp.Candidate.Group != "contractors" {
		t.Errorf("the picked group must win over the roster, got %q", resp.Candidate.Group)
	}
	if resp.Candidate.Author != "alice" {
		t.Errorf("the picked author must shape the candidate, got %q", resp.Candidate.Author)
	}
	// Both fragments: the simulated cohort's, and the author's own override.
	for _, want := range []string{"CONTRACTOR-FRAGMENT", "ALICE-FRAGMENT"} {
		if !strings.Contains(resp.Preview, want) {
			t.Errorf("preview missing %q:\n%s", want, resp.Preview)
		}
	}
	// The trace the panel renders must attribute each field to a layer.
	if len(resp.Policy) == 0 {
		t.Error("the preview must carry the policy trace the panel renders")
	}
}

// rosterServer is promptServer plus a roster, for the lookup below.
func TestPromptPreviewReadsTheAuthorsRealGroup(t *testing.T) {
	cfg := config.Config{
		Review: config.ReviewSettings{MainPrompt: "MAIN"},
		Authors: config.AuthorSettings{
			Groups: map[string]config.Group{
				"contractors": {Review: config.ReviewComment, Prompt: "CONTRACTOR-FRAGMENT"},
			},
		},
	}
	s := &Server{
		config: func() config.Config { return cfg },
		store:  &fakeStore{groups: map[string]string{"alice": "contractors"}},
	}

	code, resp := serveJSON[promptPreviewResp](t, s.handlePromptPreview, http.MethodGet,
		"/api/prompt/preview?author=alice&repo=org/repo", "")
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	if resp.Candidate.Group != "contractors" {
		t.Errorf("group = %q, want the roster's answer (contractors)", resp.Candidate.Group)
	}
	if resp.Candidate.AuthorAllowed {
		t.Error("contractors is comment-only; the preview must not claim approval is permitted")
	}
	if !strings.Contains(resp.Preview, "CONTRACTOR-FRAGMENT") {
		t.Errorf("the real group's prompt must reach the preview:\n%s", resp.Preview)
	}

	// An author with no row falls through to authors.unlisted, not to approve.
	_, unrostered := serveJSON[promptPreviewResp](t, s.handlePromptPreview, http.MethodGet,
		"/api/prompt/preview?author=nobody&repo=org/repo", "")
	if unrostered.Candidate.AuthorAllowed {
		t.Error("an unrostered author must not be previewed as approvable")
	}
}
