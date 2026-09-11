package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ScoreContext is what a scorer needs to know about a PR's history before it
// can score one review of it.
type ScoreContext struct {
	// Attempt is this review's 1-based REVISION index: the number of distinct
	// earlier head SHAs of this PR that got a real verdict, plus one.
	Attempt int
	// ReviewedAtThisHead says a real verdict was already recorded against this
	// same head. That makes the current review a discussion re-review rather
	// than a new round of code, and it must score 0: replying to the bot is
	// not work, and charging a decay step for it (or paying out again) would
	// both be wrong.
	ReviewedAtThisHead bool
}

// ScoreContext resolves the attempt index and the same-head check in one
// query. before is the instant the review being scored was recorded, so a
// recompute running later derives exactly what completion would have.
func (d *duckDB) ScoreContext(ctx context.Context, repo string, number int, headSHA string, before time.Time) (ScoreContext, error) {
	prior := fmt.Sprintf("FROM history WHERE %s AND verdict IN %s AND reviewed_at < %s",
		prWhere(repo, number), realVerdictsSQL, tsExact(before))
	sql := fmt.Sprintf(
		"SELECT (SELECT count(DISTINCT head_sha) %s AND head_sha IS DISTINCT FROM %s) AS prior_heads, "+
			"(SELECT count(*) %s AND head_sha IS NOT DISTINCT FROM %s) AS same_head",
		prior, nullText(headSHA), prior, nullText(headSHA))

	got, ok, err := queryOne(ctx, d, sql, func(m map[string]any) (ScoreContext, error) {
		r := &row{values: m}
		return ScoreContext{
			Attempt:            r.int("prior_heads") + 1,
			ReviewedAtThisHead: r.int("same_head") > 0,
		}, r.err
	})
	if err != nil {
		return ScoreContext{}, err
	}
	if !ok {
		return ScoreContext{Attempt: 1}, nil
	}
	return got, nil
}

// SetReviewScore freezes a score onto one history row.
//
// The row is addressed by (repo, number, reviewed_at) because history has no
// primary key and ReviewLogKey is a Go-side digest with no SQL form. That
// tuple is unique in practice (ReviewedAt is stamped per row) but nothing
// enforces it, so the write counts what it matched and refuses rather than
// scoring two rows on a coincidence.
func (d *duckDB) SetReviewScore(ctx context.Context, ref ReviewRef, s ScoreRecord) error {
	where := fmt.Sprintf("%s AND reviewed_at = %s", prWhere(ref.Repo, ref.Number), tsExact(ref.ReviewedAt))

	n, _, err := queryOne(ctx, d, "SELECT count(*) AS n FROM history WHERE "+where, scanCount)
	if err != nil {
		return err
	}
	switch {
	case n == 0:
		return fmt.Errorf("score %s#%d: no history row recorded at %s",
			ref.Repo, ref.Number, ref.ReviewedAt.UTC().Format(time.RFC3339))
	case n > 1:
		return fmt.Errorf("score %s#%d: %d history rows share the instant %s; refusing to score them all",
			ref.Repo, ref.Number, n, ref.ReviewedAt.UTC().Format(time.RFC3339))
	}

	at := s.At
	if at.IsZero() {
		at = time.Now()
	}
	return d.exec(ctx, fmt.Sprintf(
		"UPDATE history SET score = %s, score_source = %s, score_rules = %s, score_bucket = %s, score_note = %s, score_attempt = %s, scored_at = %s WHERE %s",
		intOrNull(s.Score), nullText(s.Source), nullText(s.Rules), nullText(s.Bucket), nullText(s.Note), intOrNull(s.Attempt), ts(at), where))
}

// ReviewsToScore selects history rows a scoring sweep should visit, oldest
// first so that attempt indices are resolved in the order they happened.
//
// Only real verdicts are ever candidates: SKIPPED and ERROR are outcomes of
// our own machinery, not feedback to an author, and scoring them would charge
// somebody for our rate limit.
func (d *duckDB) ReviewsToScore(ctx context.Context, q ScoreQuery) ([]Review, error) {
	where := []string{"verdict IN " + realVerdictsSQL}
	if q.Repo != "" {
		where = append(where, "repo = "+nullText(q.Repo))
	}
	if q.Number > 0 {
		where = append(where, fmt.Sprintf("number = %d", q.Number))
	}
	if q.Author != "" {
		where = append(where, "lower(author) = lower("+nullText(q.Author)+")")
	}
	if !q.Since.IsZero() {
		where = append(where, "reviewed_at >= "+ts(q.Since))
	}
	if q.Missing {
		where = append(where, "score IS NULL")
	}
	if len(q.StaleRules) > 0 {
		where = append(where, "(score_rules IS DISTINCT FROM "+currentRulesCase(q.StaleRules)+")")
	}
	// A manual score is a deliberate correction. A retune that silently undid
	// it would not be a correction, so it is skipped unless asked for.
	if !q.IncludeManual {
		where = append(where, "(score_source IS DISTINCT FROM "+nullText(ScoreManual)+")")
	}

	sql := "SELECT * FROM history WHERE " + strings.Join(where, " AND ") + " ORDER BY reviewed_at ASC"
	if q.Limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", q.Limit)
	}
	return queryMany(ctx, d, sql, scanReview)
}

// Leaderboard aggregates scores per author.
//
// Done in SQL rather than in Go over ListReviewsSince, which is how the
// dashboard's other metrics work: those summarise a 24h window, where pulling
// the rows is cheap and the arithmetic is varied. A leaderboard spans ALL
// history and asks one question of it, which is exactly what a columnar store
// is fastest at. The aggregate is deliberately not denormalised onto the
// author roster: every recompute and manual correction would have to fan out
// to keep it true, and allowed_authors is keyed (repo, handle) so somebody
// active in three repos has no single row to hold a total anyway.
//
// score IS NOT NULL is load-bearing. NULL means never scored; summing it as 0
// would let an unscored row drag a mean down and make Reviews count rows that
// contributed nothing.
func (d *duckDB) Leaderboard(ctx context.Context, q LeaderboardQuery) ([]AuthorScore, error) {
	where := []string{"score IS NOT NULL", "author IS NOT NULL", "author <> ''"}
	if q.Repo != "" {
		where = append(where, "repo = "+nullText(q.Repo))
	}
	if !q.Since.IsZero() {
		where = append(where, "reviewed_at >= "+ts(q.Since))
	}
	// One scored row per REVISION, chosen as the earliest.
	//
	// Points are per revision of the code reviewed, so counting two scored rows
	// at one head would pay twice for one piece of work. That is reachable
	// without any bug in the scoring itself: a review outrunning its claim
	// lease can be re-claimed by a second worker, and if both resolve their
	// ScoreContext before either writes history, neither sees the other and
	// both derive a full score. Deduplicating here closes it for good, in one
	// place, rather than trying to win a race between two processes that may
	// not even be on the same host. A discussion re-review at the same head
	// scores 0, so dropping it costs nothing either way.
	sql := fmt.Sprintf(`SELECT author,
	  sum(score)::BIGINT AS total,
	  count(*)::BIGINT AS reviews,
	  sum(CASE WHEN verdict = %s THEN 1 ELSE 0 END)::BIGINT AS approvals,
	  sum(COALESCE(scored_additions, 0))::BIGINT AS additions,
	  sum(COALESCE(scored_deletions, 0))::BIGINT AS deletions
	FROM (
	  SELECT * FROM history WHERE %s
	  QUALIFY row_number() OVER (PARTITION BY repo, number, head_sha ORDER BY reviewed_at) = 1
	)
	GROUP BY author
	ORDER BY total DESC, reviews DESC, author ASC`,
		nullText(VerdictApproved), strings.Join(where, " AND "))
	if q.Limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", q.Limit)
	}
	return queryMany(ctx, d, sql, scanAuthorScore)
}

// currentRulesCase renders the hash each row SHOULD carry as a CASE over its
// repo, so one query can ask "is this row scored under rules we still use"
// across repos that are scored under different rules.
//
// The "" entry is the ELSE: the hash for any repo with no override of its own.
// Repo keys are sorted so the statement is stable between runs, which keeps a
// logged query diffable and a test's expectation writable.
func currentRulesCase(byRepo map[string]string) string {
	repos := make([]string, 0, len(byRepo))
	for repo := range byRepo {
		if repo != "" {
			repos = append(repos, repo)
		}
	}
	// No overrides configured is the common case, and a CASE with no WHEN
	// branch is a syntax error rather than a degenerate one, so the plain
	// literal is both the correct and the simpler rendering.
	if len(repos) == 0 {
		return nullText(byRepo[""])
	}

	var b strings.Builder
	b.WriteString("CASE")
	sort.Strings(repos)
	for _, repo := range repos {
		fmt.Fprintf(&b, " WHEN lower(repo) = lower(%s) THEN %s", nullText(repo), nullText(byRepo[repo]))
	}
	fmt.Fprintf(&b, " ELSE %s END", nullText(byRepo[""]))
	return b.String()
}

func scanAuthorScore(m map[string]any) (AuthorScore, error) {
	r := &row{values: m}
	a := AuthorScore{
		Author:    r.str("author"),
		Total:     r.int("total"),
		Reviews:   r.int("reviews"),
		Approvals: r.int("approvals"),
		Additions: r.int("additions"),
		Deletions: r.int("deletions"),
	}
	return a, r.err
}
