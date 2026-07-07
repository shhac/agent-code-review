package cli

import (
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/store"
)

func registerApprovers(root *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "approvers",
		Short: "Manage the per-repo approver allow-list (stored in DuckDB)",
		Long: "The approver allow-list decides who a PR may be APPROVED for, per repo.\n" +
			"A PR author on the list for its repo (or the wildcard repo \"*\") may be\n" +
			"approved; anyone else is comment-only. Only this PR's author↔approvable\n" +
			"pair is ever passed to the review engine — never the whole list.",
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(approversLsCmd(), approversAddCmd(), approversRmCmd())
	root.AddCommand(cmd)
}

func approversLsCmd() *cobra.Command {
	var repo string
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List approvers (NDJSON), optionally for one repo",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withStore(func(s store.Store) error {
				approvers, err := s.ListApprovers(cmd.Context(), repo)
				if err != nil {
					return err
				}
				for _, a := range approvers {
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

func approversAddCmd() *cobra.Command {
	var name, email, slackID string
	cmd := &cobra.Command{
		Use:   "add <owner/repo|*> <github-handle>",
		Short: "Add (or update) an approver for a repo",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withStore(func(s store.Store) error {
				a := store.Approver{
					Repo:         args[0],
					GitHubHandle: args[1],
					Name:         name,
					Email:        email,
					SlackID:      slackID,
				}
				if err := s.AddApprover(cmd.Context(), a); err != nil {
					return err
				}
				return emit(map[string]any{"added": a.Repo + " / @" + a.GitHubHandle})
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "Display name")
	f.StringVar(&email, "email", "", "Email")
	f.StringVar(&slackID, "slack-id", "", "Slack user ID")
	return cmd
}

func approversRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <owner/repo|*> <github-handle>",
		Short: "Remove an approver from a repo",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withStore(func(s store.Store) error {
				if err := s.RemoveApprover(cmd.Context(), args[0], args[1]); err != nil {
					return err
				}
				return emit(map[string]any{"removed": args[0] + " / @" + args[1]})
			})
		},
	}
}
