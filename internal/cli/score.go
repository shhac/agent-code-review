package cli

import (
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
	cmd.AddCommand(scoreLsCmd(), scoreShowCmd(), scoreSetCmd(), scoreRecomputeCmd(), scoreRefetchCmd(), scoreLeaderboardCmd())
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
	_ = cmd.RegisterFlagCompletionFunc("repo", completeRepos)
	_ = cmd.RegisterFlagCompletionFunc("author", completeAuthorHandles)
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
		Use:               "show <owner/repo> <number>",
		Short:             "Show every scored review of one PR (NDJSON)",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeRepoThenNumber(false),
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
		Args:              cobra.ExactArgs(3),
		ValidArgsFunction: completeRepoThenNumber(false),
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

func scoreLeaderboardCmd() *cobra.Command {
	var repo, sort string
	var days, limit int
	cmd := &cobra.Command{
		Use:     "leaderboard",
		Aliases: []string{"board"},
		Short:   "Score per author, highest first (NDJSON)",
		Long: "Score per author, highest first.\n\n" +
			"--sort decides what leading MEANS. total is what somebody contributed;\n" +
			"mean and median describe a typical pull request of theirs, which is the\n" +
			"same question with volume normalised away. A median over one or two\n" +
			"reviews is a single PR wearing a trend's clothes, so read it next to the\n" +
			"review count rather than on its own.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !store.ValidLeaderSort(sort) {
				return fmt.Errorf("sort must be one of %s, got %q", strings.Join(store.LeaderSorts, ", "), sort)
			}
			q := store.LeaderboardQuery{Repo: repo, Limit: limit, Sort: sort}
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
	fs.StringVar(&sort, "sort", store.LeaderTotal,
		"What leading means: "+strings.Join(store.LeaderSorts, ", ")+" (mean and median normalise away volume)")
	_ = cmd.RegisterFlagCompletionFunc("repo", completeRepos)
	_ = cmd.RegisterFlagCompletionFunc("sort", completeStatic(store.LeaderSorts))
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

	// Remeasured says the exclusion policy was re-applied to the stored
	// per-file detail. False means the row kept the counts it already had,
	// because no measurement was stored to re-apply policy to.
	Remeasured bool   `json:"remeasured,omitempty"`
	Skipped    string `json:"skipped,omitempty"` // why this row was left alone
	DryRun     bool   `json:"dry_run,omitempty"`
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
