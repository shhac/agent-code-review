package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	output "github.com/shhac/lib-agent-output"
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
)

func registerScore(root *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "score",
		Short: "Author scores and the leaderboard (stored in DuckDB)",
		Long: "Every completed review earns the PR's author points, from the diff's\n" +
			"size, the verdict, whether the codebase grew or shrank, and how many\n" +
			"revisions it took. Generated and vendored files are left out of the\n" +
			"size, read from the repo's own .gitattributes.\n\n" +
			"Scores are FROZEN when a review completes, alongside a hash of the\n" +
			"rules that produced them. Retuning the multipliers therefore changes\n" +
			"what FUTURE reviews earn and leaves existing points alone; `recompute`\n" +
			"is how you deliberately re-apply new rules to old rows, and `set` is\n" +
			"how you correct one by hand.",
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(scoreLsCmd(), scoreShowCmd(), scoreSetCmd(), scoreRecomputeCmd(), scoreLeaderboardCmd())
	registerGroupUsage(cmd, "score", scoreUsageText)
	root.AddCommand(cmd)
}

// scoreFilters are the narrowing flags ls and recompute share.
type scoreFilters struct {
	repo    string
	author  string
	days    int
	missing bool
	stale   bool
}

func (f *scoreFilters) bind(cmd *cobra.Command) {
	fs := cmd.Flags()
	fs.StringVar(&f.repo, "repo", "", `Only this repo ("owner/name")`)
	fs.StringVar(&f.author, "author", "", "Only this GitHub handle")
	fs.IntVar(&f.days, "days", 0, "Only reviews from the last N days (0 = all history)")
	fs.BoolVar(&f.missing, "missing", false, "Only rows that were never scored (a fetch failed at the time)")
	fs.BoolVar(&f.stale, "stale", false, "Only rows scored under a ruleset that has since changed")
}

// query turns the flags into a store query. rulesFor resolves the CURRENT
// ruleset hash, which is what "stale" is measured against.
func (f *scoreFilters) query(cfg config.Config) store.ScoreQuery {
	q := store.ScoreQuery{Repo: f.repo, Author: f.author, Missing: f.missing}
	if f.days > 0 {
		q.Since = time.Now().AddDate(0, 0, -f.days)
	}
	if f.stale {
		// Per repo, not one hash: scoring.repos means different repos are
		// scored under different rules, and one figure would mark every row in
		// an overridden repo stale forever.
		q.StaleRules = cfg.CurrentRuleHashes()
	}
	return q
}

func scoreLsCmd() *cobra.Command {
	f := &scoreFilters{}
	var limit int
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List scored reviews, oldest first (NDJSON)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := config.Read()
			q := f.query(cfg)
			q.Limit = limit
			q.IncludeManual = true // a listing hides nothing
			return withStore(func(s store.Store) error {
				rows, err := s.ReviewsToScore(cmd.Context(), q)
				if err != nil {
					return err
				}
				return emitEach(rows, func(_ int, r store.Review) any { return scoreRow(r) })
			})
		},
	}
	f.bind(cmd)
	cmd.Flags().IntVar(&limit, "limit", 200, "Maximum rows to list (0 = no limit)")
	return cmd
}

func scoreShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <owner/repo> <number>",
		Short: "Show every scored review of one PR (NDJSON)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, number, err := parseRepoNumber(args)
			if err != nil {
				return err
			}
			return withStore(func(s store.Store) error {
				rows, err := s.ReviewsToScore(cmd.Context(),
					store.ScoreQuery{Repo: repo, Number: number, IncludeManual: true})
				if err != nil {
					return err
				}
				if len(rows) == 0 {
					return output.New(fmt.Sprintf("No reviews recorded for %s#%d", repo, number), output.FixableByAgent)
				}
				return emitEach(rows, func(_ int, r store.Review) any { return scoreRow(r) })
			})
		},
	}
	return cmd
}

func scoreSetCmd() *cobra.Command {
	var note string
	cmd := &cobra.Command{
		Use:   "set <owner/repo> <number> <score>",
		Short: "Set one PR's most recent score by hand",
		Long: "Overrides the computed score on the PR's LATEST review and marks it\n" +
			"manual, which makes it immune to `recompute` unless that is run with\n" +
			"--include-manual. A correction a later retune silently undid would not\n" +
			"be a correction.",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, number, err := parseRepoNumber(args[:2])
			if err != nil {
				return err
			}
			points, err := parseScore(args[2])
			if err != nil {
				return err
			}
			if note == "" {
				return output.New("A manual score needs --note saying why; it is the only record of the reason",
					output.FixableByAgent)
			}
			return withStore(func(s store.Store) error {
				last, ok, err := s.LastOutcome(cmd.Context(), repo, number)
				if err != nil {
					return err
				}
				if !ok {
					return output.New(fmt.Sprintf("No reviews recorded for %s#%d", repo, number), output.FixableByAgent)
				}
				rec := store.ScoreRecord{
					Score: &points, Source: store.ScoreManual, Note: note,
					Rules: last.Score.Rules, Bucket: last.Score.Bucket, Attempt: last.Score.Attempt, At: time.Now(),
				}
				if err := s.SetReviewScore(cmd.Context(), last.Ref(), rec); err != nil {
					return err
				}
				last.Score = rec
				return emit(scoreRow(last))
			})
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "Why this score was set by hand (required)")
	return cmd
}

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
		// The same derivation completion uses, so a recompute cannot produce a
		// different answer than the review would have. It also declines rows
		// whose diff describes another revision, which is why this loop needs
		// no guard of its own: forgetting one here is precisely how the two
		// paths came apart before.
		rec, ok := store.DeriveScore(cfg.ResolveScoring(r.Repo), sc, r, time.Now())
		if !ok {
			skipped++
			continue
		}
		if !dryRun {
			if err := s.SetReviewScore(ctx, r.Ref(), rec); err != nil {
				return err
			}
		}
		was := r.Score
		r.Score = rec
		row := scoreRow(r)
		row.Was = was.Score
		row.DryRun = dryRun
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

func scoreLeaderboardCmd() *cobra.Command {
	var repo string
	var days, limit int
	cmd := &cobra.Command{
		Use:     "leaderboard",
		Aliases: []string{"board"},
		Short:   "Total score per author, highest first (NDJSON)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := store.LeaderboardQuery{Repo: repo, Limit: limit}
			if days > 0 {
				q.Since = time.Now().AddDate(0, 0, -days)
			}
			return withStore(func(s store.Store) error {
				board, err := s.Leaderboard(cmd.Context(), q)
				if err != nil {
					return err
				}
				return emitEach(board, func(i int, a store.AuthorScore) any {
					return struct {
						Rank int `json:"rank"`
						store.AuthorScore
					}{Rank: i + 1, AuthorScore: a}
				})
			})
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&repo, "repo", "", `Only this repo ("owner/name")`)
	fs.IntVar(&days, "days", 0, "Only reviews from the last N days (0 = all history)")
	fs.IntVar(&limit, "limit", 0, "Maximum authors to list (0 = no limit)")
	return cmd
}

// scoreRowOut is one review as this command group reports it: the identity,
// the verdict, the size it was scored on, and the score with its provenance.
//
// Score is a pointer all the way out to the JSON: null means never scored,
// which is a different fact from a score of 0 (a PR whose every line was
// generated legitimately earns nothing), and flattening them would make the
// NDJSON lie about which rows a recompute still needs to visit.
type scoreRowOut struct {
	Repo       string    `json:"repo"`
	Number     int       `json:"number"`
	Author     string    `json:"author"`
	Title      string    `json:"title,omitempty"`
	Verdict    string    `json:"verdict"`
	ReviewedAt time.Time `json:"reviewed_at"`

	Score   *int   `json:"score"`
	Was     *int   `json:"was,omitempty"` // recompute only: the score being replaced
	Bucket  string `json:"bucket,omitempty"`
	Source  string `json:"score_source,omitempty"`
	Rules   string `json:"score_rules,omitempty"`
	Note    string `json:"score_note,omitempty"`
	Attempt *int   `json:"attempt,omitempty"`

	Additions       int `json:"additions"`
	Deletions       int `json:"deletions"`
	ScoredAdditions int `json:"scored_additions"`
	ScoredDeletions int `json:"scored_deletions"`
	ExcludedFiles   int `json:"excluded_files"`

	DryRun bool `json:"dry_run,omitempty"`
}

// scoreRow projects a history row onto the reporting shape.
func scoreRow(r store.Review) scoreRowOut {
	return scoreRowOut{
		Repo: r.Repo, Number: r.Number, Author: r.Author, Title: r.Title,
		Verdict: r.Verdict, ReviewedAt: r.ReviewedAt,
		Score: r.Score.Score, Bucket: r.Score.Bucket, Source: r.Score.Source, Rules: r.Score.Rules,
		Note: r.Score.Note, Attempt: r.Score.Attempt,
		Additions: r.Diff.Additions, Deletions: r.Diff.Deletions,
		ScoredAdditions: r.Diff.ScoredAdditions, ScoredDeletions: r.Diff.ScoredDeletions,
		ExcludedFiles: r.Diff.ExcludedFiles,
	}
}

// parseScore reads the score for a manual correction.
//
// strconv rather than fmt.Sscanf("%d"), which stops at the end of its format
// and IGNORES whatever follows: "15.5" parsed as 15 and "150x" as 150, both
// with a nil error. A silent truncation is bad anywhere and worst here, on the
// one path whose whole point is that the number is set by hand, kept against
// recompute, and never derived again.
func parseScore(s string) (int, error) {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, output.New("Score must be a whole number, got "+s, output.FixableByAgent)
	}
	return v, nil
}
