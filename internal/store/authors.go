package store

import "github.com/shhac/agent-code-review/internal/config"

// AllowedAuthor is one entry in a repo's allowed-authors list: an author whose
// PRs WE may approve (we are the reviewer; this is not about who can approve).
// Decided per repo: a PR may be approved only if its author is listed for that
// PR's repo (or for the wildcard repo "*"). The list lives in the store, not
// config, so it can be managed at runtime and vary per repo.
type AllowedAuthor struct {
	Repo         string `json:"repo"`
	GitHubHandle string `json:"github_handle"`
	Name         string `json:"name,omitempty"`
	Email        string `json:"email,omitempty"`
	SlackID      string `json:"slack_id,omitempty"`
}

// WildcardRepo as an AllowedAuthor.Repo applies the entry to every repo.
const WildcardRepo = "*"

func ValidAllowedAuthorRepo(repo string) bool {
	return repo == WildcardRepo || config.ValidRepoName(repo)
}
