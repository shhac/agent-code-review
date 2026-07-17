package cli

import (
	output "github.com/shhac/lib-agent-output"
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/store"
)

func registerAuthors(root *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "authors",
		Short: "Manage the allowed authors: whose PRs we may approve (stored in DuckDB)",
		Long: "We are the reviewer. This list controls whose PRs WE will approve, per\n" +
			"repo; it is not about who can approve. An author allowed for a PR's repo\n" +
			"(or the wildcard repo \"*\") may receive an APPROVE; everyone else is\n" +
			"comment-only. Only this PR's author↔allowed pair is ever passed to the\n" +
			"review engine, never the whole list.",
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(authorsLsCmd(), authorsAllowCmd(), authorsDenyCmd())
	registerGroupUsage(cmd, "authors", authorsUsageText)
	root.AddCommand(cmd)
}

func authorsLsCmd() *cobra.Command {
	var repo string
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List allowed authors (NDJSON), optionally for one repo",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if repo != "" && !store.ValidAllowedAuthorRepo(repo) {
				return invalidAuthorRepo(repo)
			}
			return withStore(func(s store.Store) error {
				authors, err := s.ListAllowedAuthors(cmd.Context(), repo)
				if err != nil {
					return err
				}
				return emitEach(authors, nil)
			})
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", `Filter by repo ("owner/name" or "*")`)
	_ = cmd.RegisterFlagCompletionFunc("repo", completeAllowedAuthorRepo)
	return cmd
}

func authorsAllowCmd() *cobra.Command {
	var name, email, slackID string
	cmd := &cobra.Command{
		Use:   "allow <owner/repo|*> <github-handle>",
		Short: "Allow an author's PRs to be approved for a repo (upserts)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !store.ValidAllowedAuthorRepo(args[0]) {
				return invalidAuthorRepo(args[0])
			}
			return withStore(func(s store.Store) error {
				a := store.AllowedAuthor{
					Repo:         args[0],
					GitHubHandle: args[1],
					Name:         name,
					Email:        email,
					SlackID:      slackID,
				}
				if err := s.AllowAuthor(cmd.Context(), a); err != nil {
					return err
				}
				return emit(map[string]any{"allowed": a.Repo + " / @" + a.GitHubHandle})
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "Display name")
	f.StringVar(&email, "email", "", "Email")
	f.StringVar(&slackID, "slack-id", "", "Slack user ID")
	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return completeAllowedAuthorRepo(cmd, args, toComplete)
		}
		return noFile(nil)
	}
	return cmd
}

func authorsDenyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deny <owner/repo|*> <github-handle>",
		Short: "Remove an author from the allowed list (their PRs become comment-only)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !store.ValidAllowedAuthorRepo(args[0]) {
				return invalidAuthorRepo(args[0])
			}
			return withStore(func(s store.Store) error {
				if err := s.DenyAuthor(cmd.Context(), args[0], args[1]); err != nil {
					return err
				}
				return emit(map[string]any{"denied": args[0] + " / @" + args[1]})
			})
		},
	}
	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return completeAllowedAuthorRepo(cmd, args, toComplete)
		}
		return completeAllowedAuthorHandle(cmd, args, toComplete)
	}
	return cmd
}

func invalidAuthorRepo(repo string) error {
	return output.New(`Repo must be owner/name or "*", got `+repo, output.FixableByAgent)
}
