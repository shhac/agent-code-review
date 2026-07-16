package config

import (
	"regexp"
	"slices"
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
var CandidateTypes = []string{"new", "refreshed"}

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
