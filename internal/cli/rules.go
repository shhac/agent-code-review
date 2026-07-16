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
			"bullet (approve/comment/reject) instead of the prompt body, so behaviour\n" +
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
			for i, r := range config.Read().Review.Rules {
				if err := emit(ruleRecord(i, r)); err != nil {
					return err
				}
			}
			return nil
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
			"bullet instead of the prompt body. --author-allowed and --author-not-allowed\n" +
			"are the two allow-list branches and are mutually exclusive.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			name = strings.TrimSpace(name)
			prompt = strings.TrimSpace(prompt)
			if name == "" {
				return output.New("--name is required", output.FixableByAgent)
			}
			if prompt == "" {
				return output.New("--prompt is required", output.FixableByAgent)
			}
			if authorAllowed && authorNotAllowed {
				return output.New("--author-allowed and --author-not-allowed are mutually exclusive; a rule with both can never match", output.FixableByAgent)
			}
			if authorIsGHUser && authorNotGHUser {
				return output.New("--author-is-gh-user and --author-not-gh-user are mutually exclusive; a rule with both can never match", output.FixableByAgent)
			}
			if outcome != "" && !config.ValidOutcome(outcome) {
				return output.New("--outcome must be one of "+strings.Join(config.Outcomes, ", ")+", got "+outcome, output.FixableByAgent)
			}
			if candidateType != "" && !config.ValidCandidateType(candidateType) {
				return output.New("--candidate-type must be one of "+strings.Join(config.CandidateTypes, ", ")+", got "+candidateType, output.FixableByAgent)
			}
			for _, r := range repos {
				if !config.ValidRepoName(r) {
					return output.New("--repo must be owner/name, got "+r, output.FixableByAgent)
				}
			}
			rule := config.Rule{
				Name:   name,
				Prompt: prompt,
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
			if err := config.Update(func(cfg *config.Config) error {
				for _, existing := range cfg.Review.Rules {
					if strings.EqualFold(existing.Name, name) {
						return output.New("A rule named "+name+" already exists", output.FixableByAgent).
							WithHint("remove it first (rules rm " + name + ") or pick another name")
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
	f.StringVar(&outcome, "outcome", "", "Route under this post-outcome bullet: approve|comment|reject")
	f.BoolVar(&authorAllowed, "author-allowed", false, "Only when the PR author IS on the allowed-authors list")
	f.BoolVar(&authorNotAllowed, "author-not-allowed", false, "Only when the PR author is NOT on the allowed-authors list")
	f.BoolVar(&authorIsGHUser, "author-is-gh-user", false, "Only when the PR is self-authored (author == our gh user)")
	f.BoolVar(&authorNotGHUser, "author-not-gh-user", false, "Only when the PR is NOT self-authored (author != our gh user)")
	f.StringVar(&candidateType, "candidate-type", "", "Only for this candidate kind: new|refreshed")
	f.StringArrayVar(&repos, "repo", nil, "Only for these repos (owner/name; repeatable, any-of)")
	_ = cmd.RegisterFlagCompletionFunc("outcome", func(_ *cobra.Command, _ []string, tc string) ([]string, cobra.ShellCompDirective) {
		return noFile(completePrefix(config.Outcomes, tc))
	})
	_ = cmd.RegisterFlagCompletionFunc("candidate-type", func(_ *cobra.Command, _ []string, tc string) ([]string, cobra.ShellCompDirective) {
		return noFile(completePrefix(config.CandidateTypes, tc))
	})
	_ = cmd.RegisterFlagCompletionFunc("repo", completeRepos)
	return cmd
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
				kept := cfg.Review.Rules[:0]
				for _, r := range cfg.Review.Rules {
					if strings.EqualFold(r.Name, name) {
						removed++
						continue
					}
					kept = append(kept, r)
				}
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
	cmd.ValidArgsFunction = func(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return completeRuleNames(nil, args, toComplete)
		}
		return noFile(nil)
	}
	return cmd
}
