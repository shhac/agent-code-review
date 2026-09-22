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
	"github.com/shhac/agent-code-review/internal/score"
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
	fs.StringVar(&f.author, "author", "", "Only this GitHub handle")
	// After the flags exist: registering a completion for a flag not yet
	// defined fails, and with the error discarded --author had none at all.
	_ = cmd.RegisterFlagCompletionFunc("repo", completeRepos)
	_ = cmd.RegisterFlagCompletionFunc("author", completeAnyAuthorHandle)
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

// narrowed says a selection is smaller than all of history. recompute and
// refetch both refuse an unnarrowed sweep without --all, because that is the
// "everybody's points moved and nobody asked for it" case.
func narrowed(q store.ScoreQuery) bool {
	return q.Repo != "" || q.Author != "" || !q.Since.IsZero() || q.Missing || len(q.StaleRules) > 0
}

// rescoreOutcome is what a sweep did with one row, for its closing tally.
type rescoreOutcome int

const (
	rescoreUnchanged rescoreOutcome = iota
	rescoreChanged
	rescoreSkipped
)

// rescoreTally counts a sweep's outcomes for its summary record.
type rescoreTally struct{ changed, skipped int }

func (t *rescoreTally) add(o rescoreOutcome) {
	switch o {
	case rescoreChanged:
		t.changed++
	case rescoreSkipped:
		t.skipped++
	}
}

// rescore records one re-derived score, unless this is a dry run, and reports
// the row next to the score it replaces. The diff and files are written with
// the score in one statement, so a row never carries counts its score was not
// derived from.
func rescore(ctx context.Context, s store.Store, r store.Review, files []score.FileStat, rec store.ScoreRecord, dryRun, remeasured bool) (rescoreOutcome, error) {
	if !dryRun {
		if err := s.SetReviewScoring(ctx, r.Ref(), r.Diff, files, rec); err != nil {
			return rescoreUnchanged, err
		}
	}
	was := r.Score
	r.Score = rec
	row := scoreRow(r)
	row.Was = was.Score
	row.DryRun = dryRun
	row.Remeasured = remeasured
	outcome := rescoreUnchanged
	if was.Score == nil || *was.Score != rec.Points() {
		outcome = rescoreChanged
	}
	return outcome, emit(row)
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
		Short: "Set the score of one PR's latest revision by hand",
		Long: "Overrides the computed score for the PR's LATEST revision and marks it\n" +
			"manual, which makes it immune to `recompute` unless that is run with\n" +
			"--include-manual. A correction a later retune silently undid would not\n" +
			"be a correction.\n\n" +
			"The row corrected is the FIRST review at the latest reviewed head,\n" +
			"because that is the one the leaderboard counts: a later discussion\n" +
			"re-review at the same head, or a skipped or errored run, earns nothing.",
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
			return withStore(func(s store.Store) error {
				return setScore(cmd.Context(), s, repo, number, points, note)
			})
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "Why this score was set by hand (required)")
	return cmd
}

// setScore freezes a manual score onto the row the leaderboard reads for the
// PR's latest revision.
//
// Not simply the newest row. The newest can be a SKIPPED or ERROR outcome,
// which the leaderboard would then count as a revision of its own, or a
// discussion re-review at the same head, which the leaderboard drops in favour
// of the earliest scored row there. Either way the correction landed where
// nothing reads it. The earliest real review at the latest head is the row the
// leaderboard keeps once it carries a score, so that is where it goes.
func setScore(ctx context.Context, s store.Store, repo string, number, points int, note string) error {
	if note == "" {
		return output.New("A manual score needs --note saying why; it is the only record of the reason",
			output.FixableByAgent)
	}
	rows, err := s.ReviewsToScore(ctx, store.ScoreQuery{Repo: repo, Number: number, IncludeManual: true})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return output.New(fmt.Sprintf("No completed reviews recorded for %s#%d", repo, number), output.FixableByAgent)
	}
	target := latestRevision(rows)
	rec := store.ScoreRecord{
		Score: &points, Source: store.ScoreManual, Note: note,
		Rules: target.Score.Rules, Bucket: target.Score.Bucket, Attempt: target.Score.Attempt, At: time.Now(),
	}
	if err := s.SetReviewScore(ctx, target.Ref(), rec); err != nil {
		return err
	}
	target.Score = rec
	return emit(scoreRow(target))
}

// latestRevision picks the first review at the newest review's head, from rows
// ordered oldest first as ReviewsToScore returns them.
func latestRevision(rows []store.Review) store.Review {
	head := rows[len(rows)-1].HeadSHA
	for _, r := range rows {
		if r.HeadSHA == head {
			return r
		}
	}
	return rows[len(rows)-1]
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
