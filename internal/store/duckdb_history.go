package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// realVerdictsSQL is IsRealVerdict as a SQL predicate operand, built from the
// same realVerdicts list so the Go and SQL filters cannot drift.
var realVerdictsSQL = func() string {
	quoted := make([]string, len(realVerdicts))
	for i, v := range realVerdicts {
		quoted[i] = nullText(v)
	}
	return "(" + strings.Join(quoted, ", ") + ")"
}()

func (d *duckDB) LastReview(ctx context.Context, repo string, number int) (Review, bool, error) {
	return queryOne(ctx, d, fmt.Sprintf(
		"SELECT * FROM history WHERE repo = %s AND number = %d AND verdict IN %s ORDER BY reviewed_at DESC LIMIT 1",
		nullText(repo), number, realVerdictsSQL), scanReview)
}

func (d *duckDB) LastOutcome(ctx context.Context, repo string, number int) (Review, bool, error) {
	return queryOne(ctx, d, fmt.Sprintf(
		"SELECT * FROM history WHERE repo = %s AND number = %d ORDER BY reviewed_at DESC LIMIT 1", nullText(repo), number), scanReview)
}

// searchableSQL is the text one history row is matched against, and it must
// stay the same expression the dashboard's filter box advertises: repo#number,
// title, author, verdict. Built here rather than in the handler so the SQL and
// the placeholder text have one definition between them.
//
// coalesce on every nullable column, because concatenating a NULL in SQL makes
// the WHOLE string NULL: one history row with no title would otherwise drop
// out of every search, including a search for its own author.
const searchableSQL = "lower(repo || '#' || number || ' ' || coalesce(title, '') || " +
	"' ' || coalesce(author, '') || ' ' || coalesce(verdict, ''))"

// likeEscape is the LIKE escape character, one backslash. DuckDB string
// literals take a backslash literally, so it needs no doubling inside text().
const likeEscape = `\`

// likeClause renders a case-insensitive contains-match for a user's search
// text, or the empty string when there is nothing to match.
//
// The escape character matters as much as the quoting. text() handles quotes,
// which stops the string breaking out of the literal; it does nothing about %
// and _, which are LIKE metacharacters. Without escaping them a search for
// "100%" matches every row, and "a_b" matches "axb": not an injection, but a
// search box that silently lies about what it found.
func likeClause(q string) string {
	needle := strings.ToLower(strings.TrimSpace(q))
	if needle == "" {
		return ""
	}
	// Backslash first: escaping it after % and _ would double the escapes
	// this loop just added and turn them back into literals.
	for _, meta := range []string{likeEscape, "%", "_"} {
		needle = strings.ReplaceAll(needle, meta, likeEscape+meta)
	}
	return fmt.Sprintf(" WHERE %s LIKE %s ESCAPE '%s'", searchableSQL, text("%"+needle+"%"), likeEscape)
}

// reviewOrderSQL is the newest-first ordering. Repo and number always break
// the tie, and the cursor comparison names the same three columns in the same
// order; a sort that disagreed with the cursor would skip or repeat rows at
// every page boundary landing inside a group of equal timestamps.
//
// One ordering today, so one constant. ReviewSort is already plumbed through
// the query and the cursor, so a second ordering is a branch here and a new
// constant there, and every cursor already in the wild keeps saying which
// ordering it belongs to.
const reviewOrderSQL = " ORDER BY reviewed_at DESC, repo DESC, number DESC"

// afterClause restricts a page to rows strictly after a cursor, in the sort's
// own order. Row comparison rather than three OR'd predicates: DuckDB compares
// tuples lexicographically, which is exactly the ordering above.
func afterClause(c ReviewCursor) string {
	if c.IsZero() {
		return ""
	}
	// The CAST is required, not decorative: tsExact renders a quoted literal,
	// which DuckDB types as VARCHAR, and comparing a tuple whose first member
	// is VARCHAR against one whose first column is TIMESTAMP is a binder error.
	//
	// tsExact rather than ts, and that is load-bearing: history rows carry
	// sub-second precision, so a cursor truncated to the second no longer
	// names the row it came from. The tuple comparison then re-included or
	// skipped the whole group sharing that second, which is precisely the tie
	// this clause exists to break.
	return fmt.Sprintf(" (reviewed_at, repo, number) < (CAST(%s AS TIMESTAMP), %s, %d)",
		tsExact(c.ReviewedAt), text(c.Repo), c.Number)
}

// SearchReviews returns one page of outcome history, newest first, the total
// number of rows matching the query, and a cursor for the next page.
//
// Total is what makes the search honest. The dashboard used to fetch a fixed
// 500 rows and filter them in the browser, so its search box silently meant
// "search the most recent 500 reviews": on a 5000-row history, a search for
// one author returned 3 of their 21 reviews and looked complete. Counting in
// SQL is what lets the page report how many matches there really are.
func (d *duckDB) SearchReviews(ctx context.Context, q ReviewQuery) (ReviewPage, error) {
	match := likeClause(q.Text)
	// count(*) always returns a row, so the "found" flag carries nothing here.
	total, _, err := queryOne(ctx, d, "SELECT count(*) AS n FROM history"+match, scanCount)
	if err != nil {
		return ReviewPage{}, err
	}
	// The cursor narrows the page, never the count: Total has to stay the size
	// of the whole match or the pager loses a page every time you turn one.
	reviews, err := queryMany(ctx, d,
		"SELECT * FROM history"+and(match, afterClause(q.After))+reviewOrderSQL+
			fmt.Sprintf(" LIMIT %d", q.limit()), scanReview)
	if err != nil {
		return ReviewPage{}, err
	}
	return ReviewPage{Reviews: reviews, Total: total, NextCursor: nextCursor(reviews, q)}, nil
}

// nextCursor points at the last row of a full page, and is empty for a short
// one. A short page is the end of the results, and handing back a cursor there
// would offer a "next" that can only ever be empty.
func nextCursor(reviews []Review, q ReviewQuery) string {
	if len(reviews) < q.limit() {
		return ""
	}
	last := reviews[len(reviews)-1]
	return ReviewCursor{
		Sort: q.sort(), Text: q.Text,
		ReviewedAt: last.ReviewedAt, Repo: last.Repo, Number: last.Number,
	}.String()
}

// and joins the two optional predicates into one WHERE clause. likeClause
// renders its own WHERE (it is the common case and usually the only one), so
// the cursor clause is appended with AND when both are present.
func and(where, extra string) string {
	switch {
	case extra == "":
		return where
	case where == "":
		return " WHERE" + extra
	default:
		return where + " AND" + extra
	}
}

func (d *duckDB) ReviewByLogKey(ctx context.Context, repo string, number int, logKey string) (Review, bool, error) {
	rows, err := d.query(ctx, fmt.Sprintf(
		"SELECT * FROM history WHERE %s ORDER BY reviewed_at DESC", prWhere(repo, number)))
	if err != nil {
		return Review{}, false, err
	}
	for _, values := range rows {
		r, err := scanReview(values)
		if err != nil {
			return Review{}, false, err
		}
		if r.LogKey == logKey {
			return r, true, nil
		}
	}
	return Review{}, false, nil
}

func (d *duckDB) ListReviewsSince(ctx context.Context, since time.Time) ([]Review, error) {
	// Zero means "no lower bound", matching FreshTokens below; without the
	// guard ts(zero) renders NULL and `>= NULL` silently matches nothing.
	sql := "SELECT * FROM history"
	if !since.IsZero() {
		sql += fmt.Sprintf(" WHERE reviewed_at >= %s", ts(since))
	}
	return queryMany(ctx, d, sql+" ORDER BY reviewed_at", scanReview)
}

func (d *duckDB) FreshTokens(ctx context.Context, since time.Time) (int64, error) {
	sql := "SELECT COALESCE(SUM(fresh_tokens), 0) AS n FROM history"
	if !since.IsZero() {
		sql += fmt.Sprintf(" WHERE reviewed_at >= %s", ts(since))
	}
	total, _, err := queryOne(ctx, d, sql, scanCount)
	return int64(total), err
}

// AppendHistory records an outcome WITHOUT touching the queue, which is the
// whole difference from Complete: an abandoned review still has work pending,
// so its row has to stay queued. The verdict is ERROR, which is deliberately
// not a "real" verdict, so the abandoned attempt cannot pass for a review that
// happened.
func (d *duckDB) AppendHistory(ctx context.Context, r Review) error {
	return d.exec(ctx, historyInsert(r))
}
