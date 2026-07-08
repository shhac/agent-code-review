package store

import (
	"context"
	"fmt"
)

func (d *duckDB) AllowAuthor(ctx context.Context, a AllowedAuthor) error {
	sql := fmt.Sprintf(`INSERT INTO allowed_authors (repo, github_handle, name, email, slack_id)
	VALUES (%s, %s, %s, %s, %s)
	ON CONFLICT (repo, github_handle) DO UPDATE SET
	  name = excluded.name, email = excluded.email, slack_id = excluded.slack_id`,
		q(a.Repo), q(a.GitHubHandle), q(a.Name), q(a.Email), q(a.SlackID))
	_, err := d.query(ctx, sql)
	return err
}

func (d *duckDB) DenyAuthor(ctx context.Context, repo, handle string) error {
	_, err := d.query(ctx, fmt.Sprintf(
		"DELETE FROM allowed_authors WHERE repo = %s AND lower(github_handle) = lower(%s)", q(repo), q(handle)))
	return err
}

func (d *duckDB) ListAllowedAuthors(ctx context.Context, repo string) ([]AllowedAuthor, error) {
	sql := "SELECT * FROM allowed_authors"
	if repo != "" {
		sql += " WHERE repo = " + q(repo)
	}
	// Alphabetical by author (the entity this list is about), case-insensitive
	// — DuckDB's default TEXT ordering would sort "Zed" before "alice". Repo
	// breaks ties for handles allowed in several places.
	sql += " ORDER BY lower(github_handle), lower(repo)"
	rows, err := d.query(ctx, sql)
	if err != nil {
		return nil, err
	}
	return mapRows(rows, scanAuthor), nil
}

func (d *duckDB) IsAuthorAllowed(ctx context.Context, repo, handle string) (bool, error) {
	if handle == "" {
		return false, nil
	}
	rows, err := d.query(ctx, fmt.Sprintf(
		"SELECT 1 FROM allowed_authors WHERE (repo = %s OR repo = %s) AND lower(github_handle) = lower(%s) LIMIT 1",
		q(repo), q(WildcardRepo), q(handle)))
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}
