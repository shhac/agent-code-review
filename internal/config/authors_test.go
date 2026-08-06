package config

import (
	"strings"
	"testing"
)

// groupCfg is the fixture the cascade tests resolve against: three cohorts
// covering all three review levels, an unlisted fallback that differs per
// repo, and one override narrowed to a single repo.
func groupCfg() Config {
	return Config{
		Repos: []string{"acme/backend", "acme/infra"},
		Authors: AuthorSettings{
			Unlisted: map[string]string{"*": "outsider", "acme/infra": "nobody"},
			Groups: map[string]Group{
				"core":     {Review: ReviewApprove, Engine: "claude", Model: "opus", Effort: "high"},
				"outsider": {Review: ReviewComment, Prompt: "State our conventions explicitly."},
				"nobody":   {Review: ReviewIgnore},
			},
			Overrides: []AuthorOverride{{
				Handle: "author-b",
				Repos:  []string{"acme/backend"},
				Group:  Group{Model: "claude-opus-5", Effort: "medium", Prompt: "Call them Lizard Elder."},
			}},
		},
	}
}

func TestResolvePolicyCascade(t *testing.T) {
	cfg := groupCfg()
	member := func(group, repo string) Membership { return Membership{Group: group, Repo: repo} }

	tests := []struct {
		name   string
		repo   string
		handle string
		m      Membership
		want   Policy
	}{
		{
			name: "unlisted author falls to the repo's fallback group",
			repo: "acme/backend", handle: "stranger",
			want: Policy{Group: "outsider", Review: ReviewComment, Prompt: "State our conventions explicitly."},
		},
		{
			name: "per-repo unlisted entry beats the wildcard entry",
			repo: "acme/infra", handle: "stranger",
			want: Policy{Group: "nobody", Review: ReviewIgnore},
		},
		{
			name: "membership wins over the unlisted fallback",
			repo: "acme/infra", handle: "author-a", m: member("core", "*"),
			want: Policy{Group: "core", Review: ReviewApprove, Engine: "claude", Model: "opus", Effort: "high"},
		},
		{
			name: "an override patches the group field by field",
			repo: "acme/backend", handle: "author-b", m: member("core", "acme/backend"),
			want: Policy{
				Group: "core", Review: ReviewApprove, Engine: "claude",
				Model: "claude-opus-5", Effort: "medium", Prompt: "Call them Lizard Elder.",
			},
		},
		{
			name: "the override does not apply outside its repo scope",
			repo: "acme/infra", handle: "author-b", m: member("core", "*"),
			want: Policy{Group: "core", Review: ReviewApprove, Engine: "claude", Model: "opus", Effort: "high"},
		},
		{
			name: "handles match case-insensitively, as GitHub does",
			repo: "acme/backend", handle: "AUTHOR-B", m: member("core", "acme/backend"),
			want: Policy{
				Group: "core", Review: ReviewApprove, Engine: "claude",
				Model: "claude-opus-5", Effort: "medium", Prompt: "Call them Lizard Elder.",
			},
		},
		{
			name: "an override reaches an unlisted author too, and prompts accumulate",
			repo: "acme/backend", handle: "author-b",
			want: Policy{
				Group: "outsider", Review: ReviewComment,
				Model: "claude-opus-5", Effort: "medium",
				Prompt: "State our conventions explicitly.\n\nCall them Lizard Elder.",
			},
		},
		{
			name: "a membership naming a group nobody defined lands on comment, not approve",
			repo: "acme/backend", handle: "author-c", m: member("deleted-group", "*"),
			want: Policy{Group: "deleted-group", Review: ReviewComment},
		},
		{
			name: "the built-in groups need no declaration",
			repo: "acme/backend", handle: "author-d", m: member(GroupApprover, "*"),
			want: Policy{Group: GroupApprover, Review: ReviewApprove},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cfg.ResolvePolicy(tt.repo, tt.handle, tt.m); got != tt.want {
				t.Errorf("ResolvePolicy(%q, %q, %+v)\n got %+v\nwant %+v", tt.repo, tt.handle, tt.m, got, tt.want)
			}
		})
	}
}

// The legacy config keeps its exact prior meaning: a repo on
// allowed_authors_only_repos discovered nothing from an unlisted author, and
// every other repo reviewed them comment-only. Nobody has to edit config to
// keep what they had.
func TestResolvePolicyLegacyFallback(t *testing.T) {
	cfg := Config{
		Repos:                   []string{"acme/backend", "acme/infra"},
		AllowedAuthorsOnlyRepos: []string{"acme/infra"},
	}

	scoped := cfg.ResolvePolicy("acme/infra", "stranger", Membership{})
	if scoped.Review != ReviewIgnore {
		t.Errorf("author-scoped repo should ignore unlisted authors, got %q", scoped.Review)
	}
	open := cfg.ResolvePolicy("acme/backend", "stranger", Membership{})
	if open.Review != ReviewComment {
		t.Errorf("open repo should comment-review unlisted authors, got %q", open.Review)
	}
	// A legacy allow-list row, which the store's migration backfills to the
	// built-in approver group.
	listed := cfg.ResolvePolicy("acme/infra", "author-a", Membership{Group: GroupApprover, Repo: "*"})
	if !listed.MayApprove() {
		t.Errorf("a backfilled allow-list row should still be approvable, got %q", listed.Review)
	}
	// An explicit unlisted entry supersedes the legacy list.
	cfg.Authors.Unlisted = map[string]string{"acme/infra": GroupCommenter}
	if got := cfg.ResolvePolicy("acme/infra", "stranger", Membership{}); got.Review != ReviewComment {
		t.Errorf("authors.unlisted should supersede allowed_authors_only_repos, got %q", got.Review)
	}
}

func TestExplainPolicyNamesTheDecidingLayer(t *testing.T) {
	cfg := groupCfg()
	_, trace := cfg.ExplainPolicy("acme/backend", "author-b", Membership{Group: "core", Repo: "acme/backend"})

	want := map[string]string{
		"group":  "membership(acme/backend)",
		"review": "group[core]",
		"engine": "group[core]",
		"model":  "override[author-b]",
		"effort": "override[author-b]",
		"prompt": "override[author-b]",
	}
	// The last step for a field is the layer that won it.
	got := map[string]string{}
	for _, step := range trace {
		got[step.Field] = step.Source
	}
	for field, source := range want {
		if got[field] != source {
			t.Errorf("%s decided by %q, want %q (trace: %+v)", field, got[field], source, trace)
		}
	}
	if len(trace) == 0 {
		t.Fatal("ExplainPolicy returned no trace")
	}
	// ResolvePolicy must agree with ExplainPolicy; one resolution, two views.
	if p, _ := cfg.ExplainPolicy("acme/backend", "author-b", Membership{Group: "core", Repo: "acme/backend"}); p != cfg.ResolvePolicy("acme/backend", "author-b", Membership{Group: "core", Repo: "acme/backend"}) {
		t.Error("ExplainPolicy and ResolvePolicy disagreed")
	}
}

func TestWithPolicyAppliesOnlyTheResolvedEnginesDials(t *testing.T) {
	base := ReviewSettings{
		Engine: "codex",
		Codex:  CodexSettings{Model: "gpt-5.6", Effort: "low"},
		Claude: ClaudeSettings{Model: "sonnet", Effort: "low"},
	}

	t.Run("switching engine moves the dials to that engine", func(t *testing.T) {
		got := base.WithPolicy(Policy{Engine: "claude", Model: "opus", Effort: "high"})
		if got.Engine != "claude" || got.Claude.Model != "opus" || got.Claude.Effort != "high" {
			t.Errorf("claude settings not applied: %+v", got.Claude)
		}
		if got.Codex.Model != "gpt-5.6" || got.Codex.Effort != "low" {
			t.Errorf("codex settings should be untouched, got %+v", got.Codex)
		}
	})

	t.Run("dials without an engine land on the configured one", func(t *testing.T) {
		got := base.WithPolicy(Policy{Model: "gpt-5.7"})
		if got.Engine != "codex" || got.Codex.Model != "gpt-5.7" {
			t.Errorf("codex model not applied: %+v", got)
		}
		if got.Claude.Model != "sonnet" {
			t.Errorf("claude settings should be untouched, got %+v", got.Claude)
		}
	})

	t.Run("an empty policy changes nothing", func(t *testing.T) {
		got := base.WithPolicy(Policy{})
		if got.Engine != base.Engine ||
			got.Codex.Model != base.Codex.Model || got.Codex.Effort != base.Codex.Effort ||
			got.Claude.Model != base.Claude.Model || got.Claude.Effort != base.Claude.Effort {
			t.Errorf("empty policy mutated settings: %+v", got)
		}
	})

	t.Run("the default engine is used when none is configured", func(t *testing.T) {
		got := ReviewSettings{}.WithPolicy(Policy{Model: "gpt-5.7"})
		if got.Codex.Model != "gpt-5.7" {
			t.Errorf("model should land on the default engine, got %+v", got)
		}
	})
}

func TestReachableEnginesAndTheirUsers(t *testing.T) {
	cfg := groupCfg()
	cfg.Review.Engine = "codex"

	engines := cfg.ReachableEngines()
	if len(engines) != 2 || engines[0] != "codex" {
		t.Fatalf("want [codex claude] with the default first, got %v", engines)
	}
	if !contains(engines, "claude") {
		t.Errorf("claude is reachable via group core, got %v", engines)
	}

	users := cfg.GroupsUsing("claude")
	if len(users) != 1 || users[0] != "group core" {
		t.Errorf("want claude attributed to group core, got %v", users)
	}
	if got := cfg.GroupsUsing("codex"); len(got) != 1 || got[0] != "(default)" {
		t.Errorf("want codex attributed to the default, got %v", got)
	}
}

func TestValidateAuthors(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string // substring of the expected problem; "" = no problems
	}{
		{name: "a valid config reports nothing", cfg: groupCfg()},
		{
			name: "unknown review level",
			cfg:  Config{Authors: AuthorSettings{Groups: map[string]Group{"core": {Review: "maybe"}}}},
			want: `authors.groups.core.review is "maybe"`,
		},
		{
			name: "unknown engine on a group",
			cfg:  Config{Authors: AuthorSettings{Groups: map[string]Group{"core": {Engine: "gemini"}}}},
			want: `authors.groups.core.engine is "gemini"`,
		},
		{
			name: "unlisted pointing at a group nobody defined",
			cfg:  Config{Authors: AuthorSettings{Unlisted: map[string]string{"*": "ghosts"}}},
			want: `authors.unlisted[*] names group "ghosts"`,
		},
		{
			name: "unlisted keyed on something that is not a repo",
			cfg:  Config{Authors: AuthorSettings{Unlisted: map[string]string{"backend": GroupCommenter}}},
			want: `authors.unlisted key "backend" is not a repo`,
		},
		{
			name: "override with no handle",
			cfg:  Config{Authors: AuthorSettings{Overrides: []AuthorOverride{{Group: Group{Model: "opus"}}}}},
			want: "authors.overrides[0] has no handle",
		},
		{
			name: "override scoped to a malformed repo",
			cfg:  Config{Authors: AuthorSettings{Overrides: []AuthorOverride{{Handle: "a", Repos: []string{"backend"}}}}},
			want: `authors.overrides[0].repos entry "backend" is not a repo`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := tt.cfg.ValidateAuthors()
			if tt.want == "" {
				if len(problems) > 0 {
					t.Fatalf("want no problems, got %v", problems)
				}
				return
			}
			for _, p := range problems {
				if strings.Contains(p, tt.want) {
					return
				}
			}
			t.Errorf("want a problem containing %q, got %v", tt.want, problems)
		})
	}
}

func TestRepoScopeMatches(t *testing.T) {
	tests := []struct {
		scope []string
		repo  string
		want  bool
	}{
		{nil, "acme/backend", true},
		{[]string{"*"}, "acme/backend", true},
		{[]string{"acme/backend"}, "acme/backend", true},
		{[]string{"ACME/Backend"}, "acme/backend", true},
		{[]string{"acme/infra"}, "acme/backend", false},
		{[]string{"acme/infra", "acme/backend"}, "acme/backend", true},
	}
	for _, tt := range tests {
		if got := RepoScopeMatches(tt.scope, tt.repo); got != tt.want {
			t.Errorf("RepoScopeMatches(%v, %q) = %v, want %v", tt.scope, tt.repo, got, tt.want)
		}
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
