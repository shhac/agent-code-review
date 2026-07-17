package cli

import (
	"context"
	"io"
	"os"
	"time"

	output "github.com/shhac/lib-agent-output"
	"github.com/spf13/cobra"

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
				return emitEach(cands, nil)
			})
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "Filter by repo (owner/name)")
	_ = cmd.RegisterFlagCompletionFunc("repo", completeRepos)
	return cmd
}

func queueAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <owner/repo> <number>",
		Short: "Manually add a PR to the queue",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, number, err := parseRepoNumber(args)
			if err != nil {
				return err
			}
			return withStore(func(s store.Store) error {
				// Fetch real metadata up front; discovery only backfills PRs
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
	cmd.ValidArgsFunction = completeRepoThenNumber(false)
	return cmd
}

func queueRmCmd() *cobra.Command {
	cmd := &cobra.Command{
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
	cmd.ValidArgsFunction = completeRepoThenNumber(true)
	return cmd
}

func queuePromoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "promote <owner/repo> <number>",
		Short: "Review a queued PR now: float it to the top, clear any eligibility hold, treat as a manual add",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, number, err := parseRepoNumber(args)
			if err != nil {
				return err
			}
			return withStore(func(s store.Store) error {
				// The explicit "review this now" escape hatch: unlike a drag
				// reorder, this clears cooldown/settling holds and escalates
				// the row to manual (bypassing the pre-review recheck).
				if err := s.Promote(cmd.Context(), repo, number); err != nil {
					return err
				}
				return emit(map[string]any{"promoted": prKey(repo, number)})
			})
		},
	}
	cmd.ValidArgsFunction = completeRepoThenNumber(true)
	return cmd
}

func queueSkipCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skip <owner/repo> <number>",
		Short: "Skip a queued PR: record a SKIPPED outcome (re-eligible on new commits)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, number, err := parseRepoNumber(args)
			if err != nil {
				return err
			}
			return withStore(func(s store.Store) error {
				// The history row needs the queued head SHA, and a skip of a
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
	cmd.ValidArgsFunction = completeRepoThenNumber(true)
	return cmd
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
				// Stdout is the raw log stream, so the preamble is a
				// structured notice: stderr stays machine-readable.
				hint := ""
				if !follow {
					hint = "pass --follow to keep streaming as the agent writes"
				}
				output.WriteNotice(os.Stderr, "streaming "+path, hint)
				return streamFile(cmd.Context(), path, follow, os.Stdout)
			})
		},
	}
	// No -f shorthand: lib-agent-cli's persistent flag set already owns it.
	cmd.Flags().BoolVar(&follow, "follow", false, "Keep streaming as the agent writes (Ctrl-C to stop)")
	cmd.ValidArgsFunction = completeRepoThenNumber(true)
	return cmd
}

func completeRepoThenNumber(queuedOnly bool) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return completeRepos(cmd, args, toComplete)
		}
		if queuedOnly {
			return completeQueuedNumber(cmd, args, toComplete)
		}
		return noFile(nil)
	}
}

// reviewWorkDir resolves a PR's engine workspace via the shared queue-then-
// history resolution (store.FindReviewWorkspace), turning "never recorded"
// into the CLI's error envelope.
func reviewWorkDir(ctx context.Context, s store.Store, repo string, number int) (string, error) {
	ws, found, err := store.FindReviewWorkspace(ctx, s, store.ReviewLogRef{Repo: repo, Number: number})
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
