package cli

import (
	"strconv"
	"time"

	output "github.com/shhac/lib-agent-output"
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
)

func registerQueue(root *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "queue",
		Short: "Inspect and manage the review queue",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(queueLsCmd(), queueAddCmd(), queueRmCmd(), queuePromoteCmd(), queueSkipCmd())
	registerGroupUsage(cmd, "queue", queueUsageText)
	root.AddCommand(cmd)
}

func queueLsCmd() *cobra.Command {
	var status, repo string
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List queued candidates (NDJSON)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withStore(func(s store.Store) error {
				cands, err := s.ListCandidates(cmd.Context(), store.Filter{Status: status, Repo: repo})
				if err != nil {
					return err
				}
				for _, c := range cands {
					if err := emit(c); err != nil {
						return err
					}
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "Filter by status (queued|reviewing|reviewed|skipped|error)")
	cmd.Flags().StringVar(&repo, "repo", "", "Filter by repo (owner/name)")
	return cmd
}

func queueAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <owner/repo> <number>",
		Short: "Manually add a PR to the queue",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, number, err := parseRepoNumber(args)
			if err != nil {
				return err
			}
			return withStore(func(s store.Store) error {
				// Requeue inserts new or flips an existing candidate back to
				// queued, preserving discovered metadata either way.
				c := store.Candidate{
					Repo:         repo,
					Number:       number,
					Type:         store.TypeNew,
					URL:          "https://github.com/" + repo + "/pull/" + strconv.Itoa(number),
					Status:       store.StatusQueued,
					DiscoveredAt: time.Now(),
				}
				if err := s.Requeue(cmd.Context(), c); err != nil {
					return err
				}
				return emit(map[string]any{"queued": repo + "#" + strconv.Itoa(number)})
			})
		},
	}
}

func queueRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <owner/repo> <number>",
		Short: "Remove a PR from the queue",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, number, err := parseRepoNumber(args)
			if err != nil {
				return err
			}
			return withStore(func(s store.Store) error {
				if err := s.RemoveCandidate(cmd.Context(), repo, number); err != nil {
					return err
				}
				return emit(map[string]any{"removed": repo + "#" + strconv.Itoa(number)})
			})
		},
	}
}

func queuePromoteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "promote <owner/repo> <number>",
		Short: "Float a PR to the top of the queue",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, number, err := parseRepoNumber(args)
			if err != nil {
				return err
			}
			return withStore(func(s store.Store) error {
				// Negative position sorts ahead of the default 0 — true top of queue.
				if err := s.SetQueuePos(cmd.Context(), repo, number, -1); err != nil {
					return err
				}
				return emit(map[string]any{"promoted": repo + "#" + strconv.Itoa(number)})
			})
		},
	}
}

func queueSkipCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "skip <owner/repo> <number>",
		Short: "Mark a PR as skipped (won't be reviewed this cycle)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, number, err := parseRepoNumber(args)
			if err != nil {
				return err
			}
			return withStore(func(s store.Store) error {
				if err := s.SetStatus(cmd.Context(), repo, number, store.StatusSkipped); err != nil {
					return err
				}
				return emit(map[string]any{"skipped": repo + "#" + strconv.Itoa(number)})
			})
		},
	}
}

// withStore opens the store, runs fn, and closes it.
func withStore(fn func(store.Store) error) error {
	s, err := openStore(config.Read())
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	return fn(s)
}

func parseRepoNumber(args []string) (string, int, error) {
	repo := args[0]
	number, err := strconv.Atoi(args[1])
	if err != nil {
		return "", 0, output.New("PR number must be an integer, got "+args[1], output.FixableByAgent)
	}
	return repo, number, nil
}
