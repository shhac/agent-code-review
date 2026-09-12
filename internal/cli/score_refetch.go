package cli

import (
	"context"
	"fmt"
	"time"

	output "github.com/shhac/lib-agent-output"
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/discover"
	"github.com/shhac/agent-code-review/internal/store"
)

func scoreRefetchCmd() *cobra.Command {
	f := &scoreFilters{}
	var dryRun, includeManual, all bool
	var limit int
	cmd := &cobra.Command{
		Use:   "refetch",
		Short: "Re-measure PRs from GitHub and rescore them",
		Long: "Repairs rows whose size was never measured, which recompute cannot:\n" +
			"recompute re-applies policy to a stored measurement, and these rows\n" +
			"have none (a rate limit at review time, a listing GitHub truncated).\n\n" +
			"The PR must still be at the head we reviewed. GitHub only serves a\n" +
			"pull request's file list at its CURRENT head, so once the head moves\n" +
			"there is no cheap way to measure what the review actually saw, and\n" +
			"crediting the newer diff would be inventing a number. Such rows are\n" +
			"reported and left alone.\n\n" +
			"Every row costs GitHub API calls, so this is narrowed and limited by\n" +
			"default. Start with --dry-run.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := config.Read()
			q := f.query(cfg)
			q.IncludeManual = includeManual
			q.Limit = limit
			if !all && q.Repo == "" && q.Author == "" && q.Since.IsZero() && !q.Missing && len(q.StaleRules) == 0 {
				return output.New(
					"Refusing to refetch every review ever recorded without --all; narrow it with --repo, --author, --days, --missing or --stale first",
					output.FixableByAgent)
			}
			return withStore(func(s store.Store) error {
				return refetch(cmd.Context(), s, cfg, discover.Measurer{}, q, dryRun)
			})
		},
	}
	f.bind(cmd)
	fs := cmd.Flags()
	fs.BoolVar(&dryRun, "dry-run", false, "Print what would change and write nothing")
	fs.BoolVar(&includeManual, "include-manual", false, "Also overwrite scores that were set by hand")
	fs.BoolVar(&all, "all", false, "Refetch every recorded review (required when no filter is given)")
	fs.IntVar(&limit, "limit", 100, "Maximum rows to refetch (0 = no limit); each one costs API calls")
	return cmd
}

// refetch re-measures each selected row from GitHub and rescores it.
//
// Unlike recompute, which is arithmetic over what is already stored, this
// talks to GitHub once per row. It is therefore limited by default, and it
// declines any row whose PR has moved past the revision we reviewed rather
// than measuring a diff that review never saw.
func refetch(ctx context.Context, s store.Store, cfg config.Config, m discover.Measurer, q store.ScoreQuery, dryRun bool) error {
	rows, err := s.ReviewsToScore(ctx, q)
	if err != nil {
		return err
	}
	changed, skipped := 0, 0
	for _, r := range rows {
		measurement, err := m.Measure(ctx, cfg, r.Repo, r.Number)
		if err != nil {
			// One unreachable PR (deleted repo, revoked access, a rate limit)
			// must not abandon the rest of the sweep.
			if emitErr := emit(refetchSkip(r, "could not measure: "+err.Error())); emitErr != nil {
				return emitErr
			}
			skipped++
			continue
		}
		if measurement.Stats.DiffSHA != r.HeadSHA {
			if emitErr := emit(refetchSkip(r, fmt.Sprintf(
				"PR has moved to %s since it was reviewed at %s; its diff at that revision is no longer cheaply measurable",
				shortSHA(measurement.Stats.DiffSHA), shortSHA(r.HeadSHA)))); emitErr != nil {
				return emitErr
			}
			skipped++
			continue
		}

		sc, err := s.ScoreContext(ctx, r.Repo, r.Number, r.HeadSHA, r.ReviewedAt)
		if err != nil {
			return err
		}
		was := r.Score
		r.Diff = measurement.Stats
		rec, ok := store.DeriveScore(cfg.ResolveScoring(r.Repo), sc, r, time.Now())
		if !ok {
			if emitErr := emit(refetchSkip(r, "measured, but still not scorable")); emitErr != nil {
				return emitErr
			}
			skipped++
			continue
		}
		if !dryRun {
			if err := s.SetReviewScoring(ctx, r.Ref(), r.Diff, rec); err != nil {
				return err
			}
		}
		r.Score = rec
		row := scoreRow(r)
		row.Was = was.Score
		row.DryRun = dryRun
		row.Remeasured = true
		if was.Score == nil || *was.Score != rec.Points() {
			changed++
		}
		if err := emit(row); err != nil {
			return err
		}
	}
	return emit(map[string]any{
		"refetched": len(rows) - skipped, "changed": changed, "skipped": skipped, "dry_run": dryRun,
	})
}

// refetchSkip reports a row this sweep declined, and why. Skips are emitted
// rather than counted silently: the reason is the whole value of the run for
// a row that cannot be repaired.
func refetchSkip(r store.Review, reason string) scoreRowOut {
	row := scoreRow(r)
	row.Skipped = reason
	return row
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
