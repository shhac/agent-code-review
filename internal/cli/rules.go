package cli

import (
	"strings"

	output "github.com/shhac/lib-agent-output"
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
)

// rulesCmd builds the `prompts rules` group. Rules are conditional fragments
// of the prompt, so they live under `prompts` alongside the slots and preview,
// not as a top-level sibling of repos/authors.
func rulesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rules",
		Short: "Manage conditional prompt rules (stored in config.json)",
		Long: "Rules add EXTRA instructions to the assembled prompt when their condition\n" +
			"matches a candidate. With an 'outcome' they attach under that post-outcome\n" +
			"section (approve/comment/reject) instead of the prompt body, so behaviour\n" +
			"can branch on the allowed-authors list deterministically. Comment-only vs\n" +
			"approval itself is a built-in directive; you never need a rule for it.",
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(rulesLsCmd(), rulesAddCmd(), rulesRmCmd())
	registerGroupUsage(cmd, "prompts rules", rulesUsageText)
	return cmd
}

// ruleRecord is the NDJSON shape for `rules ls`: the rule flattened so every
// set condition is visible without digging into a nested object.
func ruleRecord(index int, r config.Rule) map[string]any {
	rec := map[string]any{"index": index, "name": r.Name, "prompt": r.Prompt}
	if r.When.Outcome != "" {
		rec["outcome"] = r.When.Outcome
	}
	if r.When.AuthorAllowed {
		rec["author_allowed"] = true
	}
	if r.When.AuthorNotAllowed {
		rec["author_not_allowed"] = true
	}
	if r.When.AuthorIsGHUser {
		rec["author_is_gh_user"] = true
	}
	if r.When.AuthorNotGHUser {
		rec["author_not_gh_user"] = true
	}
	if r.When.CandidateType != "" {
		rec["candidate_type"] = r.When.CandidateType
	}
	if len(r.When.Repos) > 0 {
		rec["repos"] = r.When.Repos
	}
	return rec
}

func rulesLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List configured rules (NDJSON, in config order)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return emitEach(config.Read().Review.Rules, func(i int, r config.Rule) any {
				return ruleRecord(i, r)
			})
		},
	}
}

func rulesAddCmd() *cobra.Command {
	var (
		name, prompt, outcome, candidateType string
		authorAllowed, authorNotAllowed      bool
		authorIsGHUser, authorNotGHUser      bool
		repos                                []string
	)
	cmd := &cobra.Command{
		Use:   "add --name N --prompt TEXT [--outcome approve|comment|reject] [conditions]",
		Short: "Add a conditional prompt rule",
		Long: "Append a rule. --prompt is the fragment to add; the condition flags gate\n" +
			"when it fires (unset = wildcard). --outcome routes it under a post-outcome\n" +
			"section instead of the prompt body. --author-allowed and --author-not-allowed\n" +
			"are the two allow-list branches and are mutually exclusive.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			rule := config.Rule{
				Name:   strings.TrimSpace(name),
				Prompt: strings.TrimSpace(prompt),
				When: config.Condition{
					AuthorIsGHUser:   authorIsGHUser,
					AuthorNotGHUser:  authorNotGHUser,
					AuthorAllowed:    authorAllowed,
					AuthorNotAllowed: authorNotAllowed,
					CandidateType:    candidateType,
					Repos:            repos,
					Outcome:          outcome,
				},
			}
			if err := validateRule(rule); err != nil {
				return err
			}
			if err := config.Update(func(cfg *config.Config) error {
				for _, existing := range cfg.Review.Rules {
					if strings.EqualFold(existing.Name, rule.Name) {
						return output.New("A rule named "+rule.Name+" already exists", output.FixableByAgent).
							WithHint("remove it first (rules rm " + rule.Name + ") or pick another name")
					}
				}
				cfg.Review.Rules = append(cfg.Review.Rules, rule)
				return nil
			}); err != nil {
				return err
			}
			return emit(ruleRecord(len(config.Read().Review.Rules)-1, rule))
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "Unique rule name (used by rules rm)")
	f.StringVar(&prompt, "prompt", "", "The instruction fragment to add when the rule matches")
	f.StringVar(&outcome, "outcome", "", "Route under this post-outcome section: approve|comment|reject")
	f.BoolVar(&authorAllowed, "author-allowed", false, "Only when the PR author IS on the allowed-authors list")
	f.BoolVar(&authorNotAllowed, "author-not-allowed", false, "Only when the PR author is NOT on the allowed-authors list")
	f.BoolVar(&authorIsGHUser, "author-is-gh-user", false, "Only when the PR is self-authored (author == our gh user)")
	f.BoolVar(&authorNotGHUser, "author-not-gh-user", false, "Only when the PR is NOT self-authored (author != our gh user)")
	f.StringVar(&candidateType, "candidate-type", "", "Only for this candidate kind: new|refreshed")
	f.StringArrayVar(&repos, "repo", nil, "Only for these repos (owner/name; repeatable, any-of)")
	_ = cmd.RegisterFlagCompletionFunc("outcome", completeStatic(config.Outcomes))
	_ = cmd.RegisterFlagCompletionFunc("candidate-type", completeStatic(config.CandidateTypes))
	_ = cmd.RegisterFlagCompletionFunc("repo", completeRepos)
	return cmd
}

// validateRule checks a rule is well-formed and internally consistent: required
// fields present, no mutually-exclusive condition pair set (which could never
// match), and valid enum/repo values. Pure and table-testable, kept apart from
// the persist/emit transport in rulesAddCmd.
func validateRule(r config.Rule) error {
	if r.Name == "" {
		return output.New("--name is required", output.FixableByAgent)
	}
	if r.Prompt == "" {
		return output.New("--prompt is required", output.FixableByAgent)
	}
	if r.When.AuthorAllowed && r.When.AuthorNotAllowed {
		return output.New("--author-allowed and --author-not-allowed are mutually exclusive; a rule with both can never match", output.FixableByAgent)
	}
	if r.When.AuthorIsGHUser && r.When.AuthorNotGHUser {
		return output.New("--author-is-gh-user and --author-not-gh-user are mutually exclusive; a rule with both can never match", output.FixableByAgent)
	}
	if r.When.Outcome != "" && !config.ValidOutcome(r.When.Outcome) {
		return invalidEnum("--outcome", config.Outcomes, r.When.Outcome)
	}
	if r.When.CandidateType != "" && !config.ValidCandidateType(r.When.CandidateType) {
		return invalidEnum("--candidate-type", config.CandidateTypes, r.When.CandidateType)
	}
	for _, repo := range r.When.Repos {
		if !config.ValidRepoName(repo) {
			return invalidRepo(repo)
		}
	}
	return nil
}

func rulesRmCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove rule(s) by name (case-insensitive)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			removed := 0
			if err := config.Update(func(cfg *config.Config) error {
				var kept []config.Rule
				kept, removed = filterFold(cfg.Review.Rules, func(r config.Rule) string { return r.Name }, name)
				if removed == 0 {
					return output.New("No rule named "+name, output.FixableByAgent).
						WithHint("run 'agent-code-review rules ls' to see the configured rules")
				}
				cfg.Review.Rules = kept
				return nil
			}); err != nil {
				return err
			}
			return emit(map[string]any{"removed": name, "count": removed})
		},
	}
	cmd.ValidArgsFunction = completePositional(completeRuleNames, nil)
	return cmd
}
