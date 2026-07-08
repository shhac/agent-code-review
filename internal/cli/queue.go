package cli

import (
	"context"
	"io"
	"os"
	"strconv"
	"time"

	output "github.com/shhac/lib-agent-output"
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/discover"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
)

func registerQueue(root *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "queue",
		Short: "Inspect and manage the review queue",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(queueLsCmd(), queueAddCmd(), queueRmCmd(), queuePromoteCmd(), queueSkipCmd(), queueLogCmd())
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
				return emit(map[string]any{"queued": prKey(repo, number), "title": c.Title, "author": c.Author})
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
				return emit(map[string]any{"removed": prKey(repo, number)})
			})
		},
	}
}

func queuePromoteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "promote <owner/repo> <number>",
		Short: "Review a queued PR now: float it to the top, clear any eligibility hold, treat as a manual add",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, number, err := parseRepoNumber(args)
			if err != nil {
				return err
			}
			return withStore(func(s store.Store) error {
				// The explicit "review this now" escape hatch — unlike a drag
				// reorder, this clears cooldown/settling holds and escalates
				// the row to manual (bypassing the pre-review recheck).
				if err := s.Promote(cmd.Context(), repo, number); err != nil {
					return err
				}
				return emit(map[string]any{"promoted": prKey(repo, number)})
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
					return output.New(prKey(repo, number)+" is not in the queue", output.FixableByAgent)
				}
				if err := s.Complete(cmd.Context(), store.ReviewFrom(c, "SKIPPED", store.EngineManual, time.Time{})); err != nil {
					return err
				}
				return emit(map[string]any{"skipped": prKey(repo, number)})
			})
		},
	}
}

func queueLogCmd() *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "log <owner/repo> <number>",
		Short: "Stream a review agent's log (live for in-flight reviews, kept for finished ones)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, number, err := parseRepoNumber(args)
			if err != nil {
				return err
			}
			return withStore(func(s store.Store) error {
				workDir, err := reviewWorkDir(cmd.Context(), s, repo, number)
				if err != nil {
					return err
				}
				path := review.LogPath(workDir)
				stderrLogf("streaming %s", path)
				return streamFile(cmd.Context(), path, follow, os.Stdout)
			})
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Keep streaming as the agent writes (Ctrl-C to stop)")
	return cmd
}

// reviewWorkDir resolves a PR's engine workspace via the shared queue-then-
// history resolution (store.FindWorkspace), turning "never recorded" into
// the CLI's error envelope.
func reviewWorkDir(ctx context.Context, s store.Store, repo string, number int) (string, error) {
	ws, found, err := store.FindWorkspace(ctx, s, repo, number)
	if err != nil {
		return "", err
	}
	if !found {
		return "", output.New("no review log recorded for "+prKey(repo, number), output.FixableByHuman)
	}
	return ws.Dir, nil
}

// streamFile copies the file to out; with follow it keeps polling for
// appended bytes until ctx is cancelled.
func streamFile(ctx context.Context, path string, follow bool, out io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := io.Copy(out, f); err != nil {
		return err
	}
	if !follow {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(500 * time.Millisecond):
			if _, err := io.Copy(out, f); err != nil {
				return err
			}
		}
	}
}

// prKey renders the canonical "owner/repo#N" reference used in emit keys and
// error messages.
func prKey(repo string, number int) string {
	return repo + "#" + strconv.Itoa(number)
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
	if !config.ValidRepoName(repo) {
		return "", 0, output.New("Repo must be owner/name, got "+repo, output.FixableByAgent)
	}
	number, err := strconv.Atoi(args[1])
	if err != nil {
		return "", 0, output.New("PR number must be an integer, got "+args[1], output.FixableByAgent)
	}
	if number <= 0 {
		return "", 0, output.New("PR number must be positive, got "+args[1], output.FixableByAgent)
	}
	return repo, number, nil
}
