package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	output "github.com/shhac/lib-agent-output"
	"github.com/spf13/cobra"

	"github.com/shhac/crew-code-review/internal/config"
	"github.com/shhac/crew-code-review/internal/discover"
	"github.com/shhac/crew-code-review/internal/store"
)

func queuePruneCmd() *cobra.Command {
	var repo string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "prune [--repo owner/name] [--dry-run]",
		Short: "Run the pre-review recheck now: skip stale discovered PRs, warn about merged/closed manual adds",
		Long: "Runs the recheck the scheduler would run at claim time, over the whole\n" +
			"queue and without waiting for a claim. A queue held behind a usage floor\n" +
			"never reaches a claim, so it keeps PRs that were merged or approved\n" +
			"long ago.\n\n" +
			"A discovered PR that fails the recheck is recorded as a precheck SKIPPED\n" +
			"and dropped, exactly as the scheduler would have done. A manual add is\n" +
			"only checked for being merged or closed, and is reported, never dropped:\n" +
			"remove it with queue rm. Rows being reviewed right now are left alone.\n\n" +
			"One gh call per row; no engine is run.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			cfg := config.Read()
			warnf := func(notice, hint string) { output.WriteNotice(os.Stderr, notice, hint) }
			return withStore(func(s store.Store) error {
				queue, err := s.ListQueue(ctx, repo)
				if err != nil {
					return err
				}
				login := resolveGHUser(ctx, cfg, warnf)
				p := pruner{
					store:  s,
					now:    time.Now(),
					lease:  cfg.LeaseWindow(),
					dryRun: dryRun,
					recheck: func(ctx context.Context, c store.Candidate) (bool, string, error) {
						return discover.StillCandidateAt(ctx, c.Repo, c.Number, login, discover.RecheckHead(c), cfg.RequireReviewRequest())
					},
					state: discover.PRState,
				}
				return p.sweep(ctx, queue, warnf)
			})
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "Only prune this repo (owner/name)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report what would be skipped and record nothing")
	_ = cmd.RegisterFlagCompletionFunc("repo", completeRepos)
	return cmd
}

// What prune did with one row.
const (
	pruneSkipped  = "skipped"   // discovered and stale: recorded SKIPPED and dropped
	pruneWarned   = "warned"    // manual add, merged or closed: left for a human
	pruneKept     = "kept"      // still a candidate
	pruneInFlight = "in-flight" // a live claim: the review's own recheck already ran
	pruneFailed   = "failed"    // gh could not answer; left queued
)

type pruneRow struct {
	PR     string `json:"pr"`
	Source string `json:"source"`
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

// pruner holds the two gh probes as seams, so the per-row decision is tested
// without a network.
type pruner struct {
	store   store.Store
	now     time.Time
	lease   time.Duration
	dryRun  bool
	recheck func(context.Context, store.Candidate) (bool, string, error)
	state   func(ctx context.Context, repo string, number int) (string, error)
}

func (p pruner) sweep(ctx context.Context, queue []store.Candidate, warnf func(notice, hint string)) error {
	out, err := newListStream()
	if err != nil {
		return err
	}
	tally := map[string]int{}
	for _, c := range queue {
		row, err := p.row(ctx, c)
		if err != nil {
			return err
		}
		tally[row.Action]++
		if err := out.add(row); err != nil {
			return err
		}
	}
	if n := tally[pruneWarned]; n > 0 {
		warnf(fmt.Sprintf("%d manual add(s) are merged or closed and were left queued", n),
			"crew-code-review queue rm <owner/repo> <number>")
	}
	if n := tally[pruneFailed]; n > 0 {
		warnf(fmt.Sprintf("%d row(s) could not be rechecked and were left queued", n),
			"check gh auth and connectivity, then run queue prune again")
	}
	return out.close(map[string]any{summaryKey: map[string]any{
		"checked":   len(queue),
		"skipped":   tally[pruneSkipped],
		"warned":    tally[pruneWarned],
		"kept":      tally[pruneKept],
		"in_flight": tally[pruneInFlight],
		"failed":    tally[pruneFailed],
		"dry_run":   p.dryRun,
	}})
}

// row decides one queued candidate. Only a store write fails it: one PR gh
// cannot answer for must not abandon the rest of the sweep, so that is
// reported as a row instead.
//
// A daemon may claim a row between the listing and the write. That is safe:
// its own recheck asks the same question of the same PR, so a row this skips
// is one the daemon would skip too, never one it reviews.
func (p pruner) row(ctx context.Context, c store.Candidate) (pruneRow, error) {
	row := pruneRow{PR: prKey(c.Repo, c.Number), Source: c.Source}
	if c.ClaimActive(p.now, p.lease) {
		row.Action = pruneInFlight
		return row, nil
	}
	if c.Source == store.SourceManual {
		return p.manualRow(ctx, c, row), nil
	}
	ok, reason, err := p.recheck(ctx, c)
	if err != nil {
		row.Action, row.Reason = pruneFailed, err.Error()
		return row, nil
	}
	if ok {
		row.Action = pruneKept
		return row, nil
	}
	row.Action, row.Reason = pruneSkipped, reason
	if p.dryRun {
		return row, nil
	}
	return row, p.store.Complete(ctx, store.ReviewFrom(c, store.VerdictSkipped, store.EnginePrecheck, time.Time{}))
}

// manualRow checks a manual add for being merged or closed and nothing else:
// the candidacy gates are exactly what a manual add exists to bypass, so a
// draft or an unrequested review is why it was added, not a reason to warn.
func (p pruner) manualRow(ctx context.Context, c store.Candidate, row pruneRow) pruneRow {
	state, err := p.state(ctx, c.Repo, c.Number)
	switch {
	case err != nil:
		row.Action, row.Reason = pruneFailed, err.Error()
	case state == "open":
		row.Action = pruneKept
	default:
		row.Action, row.Reason = pruneWarned, state
		row.Hint = "manual add, left queued: crew-code-review queue rm " + c.Repo + " " + strconv.Itoa(c.Number)
	}
	return row
}
