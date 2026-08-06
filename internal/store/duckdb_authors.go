package store

import (
	"context"
	"fmt"

	"github.com/shhac/agent-code-review/internal/config"
)

// sameAuthor is the identity predicate for a roster row. GitHub treats both
// repo names and handles case-insensitively, and every other comparison in
// this codebase follows suit (config.RepoMatches, lookupRepo, WatchesRepo).
// Comparing the repo exactly, as this did, made a row written "Org/Repo"
// invisible to a lookup for "org/repo": the author fell through to
// authors.unlisted and silently lost whatever group they had been given.
func sameAuthor(repo, handle string) string {
	return fmt.Sprintf("lower(repo) = lower(%s) AND lower(github_handle) = lower(%s)", q(repo), q(handle))
}

func (d *duckDB) SetAuthorGroup(ctx context.Context, a Author) error {
	// The primary key is exact text, so an upsert keyed on the literal
	// strings would leave "Org/Repo alice" and "org/repo Alice" as two rows
	// that both satisfy every lookup and race for its LIMIT 1. Retiring any
	// case-variant first keeps one row per identity, and re-inserting rather
	// than updating preserves the casing the caller last used for display.
	sql := fmt.Sprintf(`DELETE FROM allowed_authors WHERE %s;
	INSERT INTO allowed_authors (repo, github_handle, group_name, name, email, slack_id)
	VALUES (%s, %s, %s, %s, %s, %s)`,
		sameAuthor(a.Repo, a.GitHubHandle),
		q(a.Repo), q(a.GitHubHandle), q(a.Group), q(a.Name), q(a.Email), q(a.SlackID))
	return d.exec(ctx, sql)
}

func (d *duckDB) RemoveAuthor(ctx context.Context, repo, handle string) error {
	return d.exec(ctx, "DELETE FROM allowed_authors WHERE "+sameAuthor(repo, handle))
}

func (d *duckDB) ListAuthors(ctx context.Context, repo, group string) ([]Author, error) {
	sql := "SELECT * FROM allowed_authors WHERE 1 = 1"
	if repo != "" {
		sql += fmt.Sprintf(" AND lower(repo) = lower(%s)", q(repo))
	}
	if group != "" {
		sql += " AND group_name = " + q(group)
	}
	// Alphabetical by author (the entity this list is about), case-insensitive:
	// DuckDB's default TEXT ordering would sort "Zed" before "alice". Repo
	// breaks ties for handles listed in several places.
	sql += " ORDER BY lower(github_handle), lower(repo)"
	return queryMany(ctx, d, sql, scanAuthor)
}

// AuthorGroup returns the group membership that applies to handle on repo: the
// row keyed on the repo itself, else the wildcard row, else the zero value
// (the author is unlisted, and config's unlisted fallback decides). The
// precedence is decided here, in the query, rather than by the caller, so
// there is one answer to "which row wins".
func (d *duckDB) AuthorGroup(ctx context.Context, repo, handle string) (config.Membership, error) {
	if handle == "" {
		return config.Membership{}, nil
	}
	// The wildcard is a literal, not a repo name, so it is matched exactly;
	// only the real repo folds case. ORDER BY <exact repo match> DESC puts the
	// repo-specific row first (booleans sort false before true), so LIMIT 1
	// takes it over the wildcard.
	rows, err := d.query(ctx, fmt.Sprintf(
		`SELECT repo, group_name FROM allowed_authors
		 WHERE (lower(repo) = lower(%s) OR repo = %s) AND lower(github_handle) = lower(%s)
		 ORDER BY (lower(repo) = lower(%s)) DESC LIMIT 1`,
		q(repo), q(WildcardRepo), q(handle), q(repo)))
	if err != nil || len(rows) == 0 {
		return config.Membership{}, err
	}
	// A row written before group_name existed, or by a build that left it
	// null, still means what it always meant: an allow-list entry.
	group := getString(rows[0], "group_name")
	if group == "" {
		group = config.GroupApprover
	}
	return config.Membership{Group: group, Repo: getString(rows[0], "repo")}, nil
}
