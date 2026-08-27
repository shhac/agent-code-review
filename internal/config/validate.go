package config

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// repoNamePattern is the one definition of the accepted "owner/name" shape;
// the CLI and dashboard validators both consume it via ValidRepoName.
var repoNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// ValidRepoName reports whether s looks like an "owner/name" repo reference.
func ValidRepoName(s string) bool { return repoNamePattern.MatchString(s) }

// Outcomes are the post-outcome sections a rule fragment can be routed under.
// They mirror the review outcomes the agent can land on (reject = requested
// changes). SKIPPED has no prompt slot, so it is not routable.
var Outcomes = []string{"approve", "comment", "reject"}

// ValidOutcome reports whether s names a routable post-outcome section.
func ValidOutcome(s string) bool { return slices.Contains(Outcomes, s) }

// CandidateTypes are the discovery kinds a rule can gate on.
var CandidateTypes = []string{"new", "refreshed", "discussion"}

// ValidCandidateType reports whether s names a candidate discovery kind.
func ValidCandidateType(s string) bool { return slices.Contains(CandidateTypes, s) }

// RepoMatches reports whether want is in list using GitHub repo identity
// semantics (case-insensitive owner/name match).
func RepoMatches(list []string, want string) bool {
	for _, r := range list {
		if strings.EqualFold(r, want) {
			return true
		}
	}
	return false
}

// ValidateAuthors reports author-group misconfigurations that would misroute
// reviews without any single value being malformed on its own: a group with an
// unknown review level or engine, an unlisted fallback pointing at a group
// nobody defined, an override with no handle or a malformed repo scope. Empty
// means nothing statically detectable is wrong. Membership rows naming a
// deleted group cannot be seen from here (they live in the store); `authors
// ls` surfaces those by resolving each row.
func (c Config) ValidateAuthors() []string {
	var problems []string
	for _, name := range sortedKeys(c.Authors.Groups) {
		g := c.Authors.Groups[name]
		if g.Review != "" && !ValidReviewLevel(g.Review) {
			problems = append(problems, fmt.Sprintf("authors.groups.%s.review is %q; valid: %s",
				name, g.Review, strings.Join(ReviewLevels, ", ")))
		}
		problems = append(problems, engineProblem("authors.groups."+name, g.Engine)...)
	}
	for _, repo := range sortedKeys(c.Authors.Unlisted) {
		if repo != WildcardRepo && !ValidRepoName(repo) {
			problems = append(problems, fmt.Sprintf(
				`authors.unlisted key %q is not a repo; use "owner/name" or "*"`, repo))
		}
		if group := c.Authors.Unlisted[repo]; group != "" {
			if _, ok := c.Group(group); !ok {
				problems = append(problems, fmt.Sprintf(
					"authors.unlisted[%s] names group %q, which is not defined; valid: %s",
					repo, group, strings.Join(c.GroupNames(), ", ")))
			}
		}
	}
	for i, o := range c.Authors.Overrides {
		where := fmt.Sprintf("authors.overrides[%d]", i)
		if strings.TrimSpace(o.Handle) == "" {
			problems = append(problems, where+" has no handle, so it can never match an author")
		}
		if o.Review != "" && !ValidReviewLevel(o.Review) {
			problems = append(problems, fmt.Sprintf("%s.review is %q; valid: %s",
				where, o.Review, strings.Join(ReviewLevels, ", ")))
		}
		problems = append(problems, engineProblem(where, o.Engine)...)
		for _, repo := range o.Repos {
			if repo != WildcardRepo && !ValidRepoName(repo) {
				problems = append(problems, fmt.Sprintf(
					`%s.repos entry %q is not a repo; use "owner/name" or "*"`, where, repo))
			}
		}
	}
	return problems
}

func engineProblem(where, engine string) []string {
	if engine == "" || slices.Contains(EngineNames, engine) {
		return nil
	}
	return []string{fmt.Sprintf("%s.engine is %q; valid: %s", where, engine, strings.Join(EngineNames, ", "))}
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
