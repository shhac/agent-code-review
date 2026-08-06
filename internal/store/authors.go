package store

import "github.com/shhac/agent-code-review/internal/config"

// Author is one row of the roster: which group an author belongs to for a
// given repo, plus the contact details a review prompt might need. The group
// names a policy defined in config; this table holds only membership, because
// membership is the part that churns and varies per repo, while the policy it
// points at is hand-edited text that belongs beside the prompts.
//
// Decided per repo: a row for the PR's repo wins over a row for the wildcard
// repo "*". An author with no row at all resolves through the config's
// unlisted fallback instead.
type Author struct {
	Repo         string `json:"repo"`
	GitHubHandle string `json:"github_handle"`
	Group        string `json:"group"`
	Name         string `json:"name,omitempty"`
	Email        string `json:"email,omitempty"`
	SlackID      string `json:"slack_id,omitempty"`
}

// Membership is the roster row expressed as what the policy cascade consumes.
// Callers that already hold a row (listing the roster, rendering it) resolve
// through this rather than rebuilding the struct, so "which repo key did this
// row claim" is answered in one place.
func (a Author) Membership() config.Membership {
	return config.Membership{Group: a.Group, Repo: a.Repo}
}

// WildcardRepo as an Author.Repo applies the entry to every repo. Aliased from
// config, which owns the wildcard vocabulary shared with override repo scopes
// and the unlisted map, so the three cannot drift.
const WildcardRepo = config.WildcardRepo

func ValidAuthorRepo(repo string) bool {
	return repo == WildcardRepo || config.ValidRepoName(repo)
}
