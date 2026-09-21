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
		problems = append(problems, floorProblems("authors.groups."+name, g.UsageFloor)...)
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
		problems = append(problems, floorProblems(where, o.UsageFloor)...)
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

// ValidateReview checks the base review settings that no other layer does.
//
// A cohort's engine and an override's engine are both checked below, but the
// engine they all fall back to was not. An unwired name there parses, writes
// and loads fine, then fails only once a review is actually attempted, as
// `Unknown review engine` on an ERROR history row. Doctor did notice --
// ReachableEngines includes the base engine, so it probes a binary by that
// name and fails to find one -- but "engine:gemini FAILED: binary not found"
// sends you looking for a missing install rather than a typo. This says which
// it is.
func (c Config) ValidateReview() []string {
	return engineProblem("review", c.Review.Engine)
}

// floorProblems reports a cohort's usage_floor entries that would not do what
// they say. Both failures are silent and both are money: EngineCommon falls
// back to the DEFAULT engine's block for a name it does not recognise (right
// for BinFor, wrong for a gate on spend), so usage_floor keyed "Claude" or
// "claud" quietly moves codex's floor instead; and BelowFloor tests
// `floor > 0`, so a negative percentage switches the window's gate off
// altogether rather than being rejected.
//
// The engine-level keys are bounded by the CLI (`config set` takes 0..100),
// but a cohort's floors have no CLI at all -- they are hand-edited JSON that
// reaches BelowFloor unexamined, which is exactly why they are checked here.
func floorProblems(where string, floors map[string]UsageFloorLimits) []string {
	var problems []string
	for _, engine := range sortedKeys(floors) {
		if !slices.Contains(EngineNames, engine) {
			problems = append(problems, fmt.Sprintf(
				"%s.usage_floor has a %q section, which is not an engine; valid: %s",
				where, engine, strings.Join(EngineNames, ", ")))
		}
		limits := floors[engine]
		for _, w := range limits.windows() {
			if *w.Slot == nil {
				continue
			}
			if pct := **w.Slot; pct < 0 || pct > 100 {
				problems = append(problems, fmt.Sprintf(
					"%s.usage_floor.%s.%s is %d; it is a percentage remaining, so 0 (off) to 100",
					where, engine, w.Key, pct))
			}
		}
	}
	return problems
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
