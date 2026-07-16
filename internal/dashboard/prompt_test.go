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
// boolean parsing: author_allowed defaults true and flips only on exact
// "false"; author_is_gh_user defaults false and needs exact "true". The
// previewed approval policy (MAY vs DO NOT approve) must follow.
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
		{"", "allow"},                                           // author_allowed defaults true
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
