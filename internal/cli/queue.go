package cli

import (
	"context"
	"strconv"

	output "github.com/shhac/lib-agent-output"
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/discover"
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
	var repo string
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List queued candidates (NDJSON)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withStore(func(s store.Store) error {
				cands, err := s.ListQueue(cmd.Context(), repo)
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
				// Fetch real metadata up front — discovery only backfills PRs
				// that match the candidate rules, which a manual add may not.
				c, err := discover.ManualCandidate(cmd.Context(), repo, number)
				if err != nil {
					return err
				}
				if err := s.Enqueue(cmd.Context(), c); err != nil {
					return err
				}
				return emit(map[string]any{"queued": repo + "#" + strconv.Itoa(number), "title": c.Title, "author": c.Author})
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
				if err := s.Dequeue(cmd.Context(), repo, number); err != nil {
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
		Short: "Skip a queued PR: record a SKIPPED outcome (re-eligible on new commits)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, number, err := parseRepoNumber(args)
			if err != nil {
				return err
			}
			return withStore(func(s store.Store) error {
				// The history row needs the queued head SHA — and a skip of a
				// PR that isn't queued would leave a dangling outcome, so
				// missing is an error rather than a silent no-op.
				c, found, err := findQueued(cmd.Context(), s, repo, number)
				if err != nil {
					return err
				}
				if !found {
					return output.New(repo+"#"+strconv.Itoa(number)+" is not in the queue", output.FixableByAgent)
				}
				if err := s.Complete(cmd.Context(), store.ReviewFrom(c, "SKIPPED", store.EngineManual)); err != nil {
					return err
				}
				return emit(map[string]any{"skipped": repo + "#" + strconv.Itoa(number)})
			})
		},
	}
}

// findQueued locates one queue row by number within an already repo-scoped
// ListQueue result.
func findQueued(ctx context.Context, s store.Store, repo string, number int) (store.Candidate, bool, error) {
	cands, err := s.ListQueue(ctx, repo)
	if err != nil {
		return store.Candidate{}, false, err
	}
	for _, c := range cands {
		if c.Number == number {
			return c, true, nil
		}
	}
	return store.Candidate{}, false, nil
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
