package cli

import (
	"context"
	"time"

	output "github.com/shhac/lib-agent-output"
	"github.com/spf13/cobra"

	"github.com/shhac/crew-code-review/internal/config"
	"github.com/shhac/crew-code-review/internal/score"
	"github.com/shhac/crew-code-review/internal/store"
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
			if !all && !narrowed(q) {
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
	return rescoreSweep(rows, "recomputed", dryRun, func(r store.Review) (rescoreOutcome, *scoreRowOut, error) {
		return recomputeRow(ctx, s, cfg, r, dryRun)
	})
}

func recomputeRow(ctx context.Context, s store.Store, cfg config.Config, r store.Review, dryRun bool) (rescoreOutcome, *scoreRowOut, error) {
	sc, err := s.ScoreContext(ctx, r.Repo, r.Number, r.HeadSHA, r.ReviewedAt)
	if err != nil {
		return rescoreUnchanged, nil, err
	}
	rules := cfg.ResolveScoring(r.Repo)
	files, err := s.ReviewFiles(ctx, r.Ref())
	if err != nil {
		return rescoreUnchanged, nil, err
	}
	r.Diff = recountStored(r.Diff, files, rules)

	// The same derivation completion uses, so a recompute cannot produce a
	// different answer than the review would have. It also declines rows
	// whose diff describes another revision, which is why this path needs
	// no guard of its own: forgetting one here is precisely how the two
	// paths came apart before.
	rec, ok := store.DeriveScore(rules, sc, r, time.Now())
	if !ok {
		return rescoreSkipped, nil, nil
	}
	// The same files back, unchanged: recompute re-applies policy to the
	// stored evidence rather than taking new evidence, so the detail a later
	// recompute needs must survive this write.
	return rescore(ctx, s, r, files, rec, dryRun, len(files) > 0)
}

// recountStored re-applies the exclusion policy to the measurement taken at
// review time, offline. This is what makes a change to exclude_paths or
// use_gitattributes a recompute rather than a re-fetch.
//
// A row with no stored measurement keeps the counts it already has, which is
// right for an arithmetic tweak and WRONG for an exclusion change, and nothing
// here can tell the two apart. So it is reported rather than assumed:
// remeasured=false says "this row's counts are whatever they were", and
// `score refetch` is the repair.
func recountStored(diff store.DiffStats, files []score.FileStat, rules score.Rules) store.DiffStats {
	if len(files) == 0 {
		return diff
	}
	totals := score.Recount(files, rules)
	diff.ScoredAdditions = totals.Additions
	diff.ScoredDeletions = totals.Deletions
	diff.ExcludedFiles = totals.ExcludedFiles
	return diff
}
