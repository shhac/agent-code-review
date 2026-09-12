package cli

import (
	"context"
	"time"

	output "github.com/shhac/lib-agent-output"
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/score"
	"github.com/shhac/agent-code-review/internal/store"
)

func scoreRecomputeCmd() *cobra.Command {
	f := &scoreFilters{}
	var dryRun, includeManual, all bool
	cmd := &cobra.Command{
		Use:   "recompute",
		Short: "Re-derive scores under the CURRENT rules",
		Long: "Re-applies today's rules to rows that already happened, which moves\n" +
			"points somebody already has. Narrow it with --repo/--author/--days/\n" +
			"--missing/--stale, or say --all to mean all of history. Start with\n" +
			"--dry-run: it prints the before and after for every row and writes\n" +
			"nothing.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := config.Read()
			q := f.query(cfg)
			q.IncludeManual = includeManual
			// Rescoring all of history is exactly the "everybody's points
			// moved and nobody asked for it" case, so it takes a word.
			if !all && q.Repo == "" && q.Author == "" && q.Since.IsZero() && !q.Missing && len(q.StaleRules) == 0 {
				return output.New(
					"Refusing to recompute every score ever recorded without --all; narrow it with --repo, --author, --days, --missing or --stale first",
					output.FixableByAgent)
			}
			return withStore(func(s store.Store) error {
				return recompute(cmd.Context(), s, cfg, q, dryRun)
			})
		},
	}
	f.bind(cmd)
	fs := cmd.Flags()
	fs.BoolVar(&dryRun, "dry-run", false, "Print what would change and write nothing")
	fs.BoolVar(&includeManual, "include-manual", false, "Also overwrite scores that were set by hand")
	fs.BoolVar(&all, "all", false, "Rescore all of history (required when no filter is given)")
	return cmd
}

// recompute re-derives each selected row under the current rules.
//
// The attempt index is re-derived rather than reused: a row scored late by
// --missing has none, and ScoreContext counts revisions strictly before the
// row's own instant, so it returns exactly what completion would have.
func recompute(ctx context.Context, s store.Store, cfg config.Config, q store.ScoreQuery, dryRun bool) error {
	rows, err := s.ReviewsToScore(ctx, q)
	if err != nil {
		return err
	}
	changed, skipped := 0, 0
	for _, r := range rows {
		sc, err := s.ScoreContext(ctx, r.Repo, r.Number, r.HeadSHA, r.ReviewedAt)
		if err != nil {
			return err
		}
		rules := cfg.ResolveScoring(r.Repo)

		// Re-apply the exclusion policy to the measurement taken at review
		// time, offline. This is what makes a change to exclude_paths or
		// use_gitattributes a recompute rather than a re-fetch.
		//
		// A row with no stored measurement is rescored from the counts it
		// already has, which is right for an arithmetic tweak and WRONG for an
		// exclusion change, and nothing here can tell the two apart. So it is
		// reported rather than assumed: remeasured=false says "this row's
		// counts are whatever they were", and `score refetch` is the repair.
		files, err := s.ReviewFiles(ctx, r.Ref())
		if err != nil {
			return err
		}
		if len(files) > 0 {
			totals := score.Recount(files, rules)
			r.Diff.ScoredAdditions = totals.Additions
			r.Diff.ScoredDeletions = totals.Deletions
			r.Diff.ExcludedFiles = totals.ExcludedFiles
		}

		// The same derivation completion uses, so a recompute cannot produce a
		// different answer than the review would have. It also declines rows
		// whose diff describes another revision, which is why this loop needs
		// no guard of its own: forgetting one here is precisely how the two
		// paths came apart before.
		rec, ok := store.DeriveScore(rules, sc, r, time.Now())
		if !ok {
			skipped++
			continue
		}
		if !dryRun {
			if err := s.SetReviewScoring(ctx, r.Ref(), r.Diff, rec); err != nil {
				return err
			}
		}
		was := r.Score
		r.Score = rec
		row := scoreRow(r)
		row.Was = was.Score
		row.DryRun = dryRun
		row.Remeasured = len(files) > 0
		if was.Score == nil || *was.Score != rec.Points() {
			changed++
		}
		if err := emit(row); err != nil {
			return err
		}
	}
	// skipped is reported rather than swallowed: a row left alone because its
	// diff describes another revision is a thing the operator should see, not
	// a silent difference between the count asked for and the count written.
	return emit(map[string]any{
		"recomputed": len(rows) - skipped, "changed": changed, "skipped": skipped, "dry_run": dryRun,
	})
}
