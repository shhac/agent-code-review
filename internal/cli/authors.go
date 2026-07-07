package cli

import (
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/store"
)

func registerAuthors(root *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "authors",
		Short: "Manage the allowed authors: whose PRs we may approve (stored in DuckDB)",
		Long: "We are the reviewer — this list controls whose PRs WE will approve, per\n" +
			"repo; it is not about who can approve. An author allowed for a PR's repo\n" +
			"(or the wildcard repo \"*\") may receive an APPROVE; everyone else is\n" +
			"comment-only. Only this PR's author↔allowed pair is ever passed to the\n" +
			"review engine — never the whole list.",
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
			return withStore(func(s store.Store) error {
				authors, err := s.ListAllowedAuthors(cmd.Context(), repo)
				if err != nil {
					return err
				}
				for _, a := range authors {
					if err := emit(a); err != nil {
						return err
					}
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", `Filter by repo ("owner/name" or "*")`)
	return cmd
}

func authorsAllowCmd() *cobra.Command {
	var name, email, slackID string
	cmd := &cobra.Command{
		Use:   "allow <owner/repo|*> <github-handle>",
		Short: "Allow an author's PRs to be approved for a repo (upserts)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
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
	return cmd
}

func authorsDenyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "deny <owner/repo|*> <github-handle>",
		Short: "Remove an author from the allowed list (their PRs become comment-only)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withStore(func(s store.Store) error {
				if err := s.DenyAuthor(cmd.Context(), args[0], args[1]); err != nil {
					return err
				}
				return emit(map[string]any{"denied": args[0] + " / @" + args[1]})
			})
		},
	}
}
