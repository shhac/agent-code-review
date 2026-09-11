package config

// Author scoring: the on-disk shape, and how it resolves into the flat
// ruleset internal/score actually computes with.
//
// The split is deliberate. This document is OPTIONAL everywhere: every
// multiplier is a *float64 so that an explicit 0 ("approving is worth
// nothing") is distinguishable from unset ("use the default"), and every
// field may be narrowed per repo. score.Rules is the resolved form, with no
// optionality left to reason about and, importantly, no maps: that is what
// lets score.Rules.Hash be stable without a canonicalisation step.

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/shhac/agent-code-review/internal/score"
)

// VerdictMultipliers scales a score by what the review concluded.
type VerdictMultipliers struct {
	Approved         *float64 `json:"approved,omitempty"`
	Commented        *float64 `json:"commented,omitempty"`
	RequestedChanges *float64 `json:"requested_changes,omitempty"`
}

// ScoringSettings is the scoring policy, globally and per repo.
//
// Repos holds partial overrides keyed by "owner/name". A repo entry patches
// the base field by field; its slice fields REPLACE rather than append, and a
// nested repos inside one is a configuration error rather than a silent
// no-op.
type ScoringSettings struct {
	// Mode is the global switch: ScoringEnabled, ScoringLeaderboardOnly, or
	// ScoringDisabled. Empty means enabled.
	Mode string `json:"mode,omitempty"`
	// Enabled is the pre-Mode switch, still honoured so a config written
	// against it keeps its meaning: false reads as ScoringDisabled. Mode wins
	// when both are set. New configs should use mode.
	Enabled *bool    `json:"enabled,omitempty"`
	Base    *float64 `json:"base,omitempty"`
	// ChurnUnit is how many lines Base pays for; score scales with churn in
	// units of this rather than being a flat fee per PR.
	ChurnUnit      *float64 `json:"churn_unit,omitempty"`
	DeletionWeight *float64 `json:"deletion_weight,omitempty"`
	// Buckets is score.Bucket directly rather than a config-side twin: the
	// twin was field-for-field and tag-for-tag identical, since buckets REPLACE
	// wholesale and so have no optionality to express, and it cost a conversion
	// loop plus a second place to forget a new field.
	Buckets          []score.Bucket     `json:"buckets,omitempty"`
	Verdicts         VerdictMultipliers `json:"verdicts,omitempty"`
	ShrinkBonus      *float64           `json:"shrink_bonus,omitempty"`
	AttemptDecay     *float64           `json:"attempt_decay,omitempty"`
	ExcludePaths     []string           `json:"exclude_paths,omitempty"`
	UseGitattributes *bool              `json:"use_gitattributes,omitempty"`

	Repos map[string]ScoringSettings `json:"repos,omitempty"`
}

// Scoring modes.
//
// The middle one exists because scoring's only ongoing cost is a per-review
// GitHub call to measure the diff. Turning that off should not also hide the
// points people have already earned, so stopping the work and hiding the
// results are deliberately separate switches.
const (
	ScoringEnabled         = "enabled"          // measure and score new reviews, show the leaderboard
	ScoringLeaderboardOnly = "leaderboard-only" // stop measuring; keep showing what is already scored
	ScoringDisabled        = "disabled"         // no scoring, and the leaderboard is hidden
)

// ScoringModes are the valid values of scoring.mode.
var ScoringModes = []string{ScoringEnabled, ScoringLeaderboardOnly, ScoringDisabled}

// ValidScoringMode reports whether s names a scoring mode.
func ValidScoringMode(s string) bool { return slices.Contains(ScoringModes, s) }

// ScoringMode is the effective mode for one repo.
//
// An unrecognised value reads as enabled rather than silently switching
// scoring off: a typo in a mode name must not quietly stop recording points,
// which is the failure nobody would notice. ValidateScoring reports it through
// doctor instead.
func (c Config) ScoringMode(repo string) string {
	s := c.scoringFor(repo)
	if s.Mode != "" {
		if ValidScoringMode(s.Mode) {
			return s.Mode
		}
		return ScoringEnabled
	}
	// The pre-Mode switch. Only false is meaningful: it meant "do not score",
	// which is exactly ScoringDisabled.
	if s.Enabled != nil && !*s.Enabled {
		return ScoringDisabled
	}
	return ScoringEnabled
}

// ScoringEnabled reports whether completed reviews are measured and scored.
// False in both leaderboard-only and disabled, which is what removes the
// per-review GitHub call that scoring otherwise adds.
func (c Config) ScoringEnabled(repo string) bool {
	return c.ScoringMode(repo) == ScoringEnabled
}

// LeaderboardVisible reports whether the standings should be shown at all.
// Only a fully disabled mode hides them: turning off the measuring is not a
// reason to hide the points already earned.
func (c Config) LeaderboardVisible(repo string) bool {
	return c.ScoringMode(repo) != ScoringDisabled
}

// UseGitattributes reports whether the repo's own .gitattributes is consulted
// to identify generated and vendored files (default true).
func (c Config) UseGitattributes(repo string) bool {
	s := c.scoringFor(repo)
	return s.UseGitattributes == nil || *s.UseGitattributes
}

// ExcludePaths are the operator's extra globs, for repos that have not
// adopted linguist-generated themselves.
func (c Config) ExcludePaths(repo string) []string {
	return c.scoringFor(repo).ExcludePaths
}

// ResolveScoring returns the effective ruleset for one repo: defaults, with
// the global block patched over them, with the repo's own entry patched over
// that.
//
// An invalid result falls back to the shipped defaults rather than refusing to
// score, matching Read's treatment of a corrupt file: a bad multiplier must
// not wedge the reviewer. Silence would be the wrong half of that bargain
// though, so ValidateScoring reports the same problem through doctor, where
// somebody will actually see it.
func (c Config) ResolveScoring(repo string) score.Rules {
	r := applyScoring(score.DefaultRules(), c.scoringFor(repo))
	if err := r.Validate(); err != nil {
		return score.DefaultRules()
	}
	return r
}

// scoringFor merges the global block with the repo's override. Repo identity
// is case-insensitive here as everywhere else in this config (RepoMatches),
// so a "Owner/Name" key still narrows "owner/name".
func (c Config) scoringFor(repo string) ScoringSettings {
	base := c.Scoring
	override, ok := c.scoringOverride(repo)
	if !ok {
		return base
	}
	return mergeScoring(base, override)
}

// scoringOverride finds the repo's entry through the package's own
// repo-keyed lookup rather than a local scan, which also buys its determinism
// guarantee: lookupRepo sorts keys before the case-insensitive comparison, so
// a config carrying both "Owner/Name" and "owner/name" resolves the same way
// on every call instead of however the map happened to iterate.
func (c Config) scoringOverride(repo string) (ScoringSettings, bool) {
	s, _, ok := lookupRepo(c.Scoring.Repos, repo)
	return s, ok
}

// mergeScoring patches over onto base, field by field. Slices replace rather
// than append: a repo listing its own buckets means those buckets, and an
// appending merge could not express a SHORTER ladder than the global one.
func mergeScoring(base, over ScoringSettings) ScoringSettings {
	out := base
	out.Repos = nil // a repo entry's own repos map is meaningless; validation says so
	if over.Mode != "" {
		out.Mode = over.Mode
	}
	if over.Enabled != nil {
		out.Enabled = over.Enabled
	}
	if over.Base != nil {
		out.Base = over.Base
	}
	if over.ChurnUnit != nil {
		out.ChurnUnit = over.ChurnUnit
	}
	if over.DeletionWeight != nil {
		out.DeletionWeight = over.DeletionWeight
	}
	if over.ShrinkBonus != nil {
		out.ShrinkBonus = over.ShrinkBonus
	}
	if over.AttemptDecay != nil {
		out.AttemptDecay = over.AttemptDecay
	}
	if over.UseGitattributes != nil {
		out.UseGitattributes = over.UseGitattributes
	}
	if over.Buckets != nil {
		out.Buckets = over.Buckets
	}
	if over.ExcludePaths != nil {
		out.ExcludePaths = over.ExcludePaths
	}
	if over.Verdicts.Approved != nil {
		out.Verdicts.Approved = over.Verdicts.Approved
	}
	if over.Verdicts.Commented != nil {
		out.Verdicts.Commented = over.Verdicts.Commented
	}
	if over.Verdicts.RequestedChanges != nil {
		out.Verdicts.RequestedChanges = over.Verdicts.RequestedChanges
	}
	return out
}

// applyScoring lays a settings document over a ruleset.
func applyScoring(r score.Rules, s ScoringSettings) score.Rules {
	r.Base = floatOr(s.Base, r.Base)
	r.ChurnUnit = floatOr(s.ChurnUnit, r.ChurnUnit)
	r.DeletionWeight = floatOr(s.DeletionWeight, r.DeletionWeight)
	r.ShrinkBonus = floatOr(s.ShrinkBonus, r.ShrinkBonus)
	r.AttemptDecay = floatOr(s.AttemptDecay, r.AttemptDecay)
	r.Approved = floatOr(s.Verdicts.Approved, r.Approved)
	r.Commented = floatOr(s.Verdicts.Commented, r.Commented)
	r.RequestedChanges = floatOr(s.Verdicts.RequestedChanges, r.RequestedChanges)
	if len(s.Buckets) > 0 {
		r.Buckets = s.Buckets
	}
	if s.UseGitattributes != nil {
		r.UseGitattributes = *s.UseGitattributes
	}
	// Sorted, so reordering the list in config.json does not change the hash
	// and fake a policy change nobody made. Copied rather than aliased for the
	// same reason Buckets is not: Rules is treated as immutable.
	r.ExcludePaths = append([]string(nil), s.ExcludePaths...)
	sort.Strings(r.ExcludePaths)
	return r
}

func floatOr(p *float64, fallback float64) float64 {
	if p == nil {
		return fallback
	}
	return *p
}

// CurrentRuleHashes is the ruleset hash each repo is scored under right now,
// keyed by repo, with "" holding the hash for any repo that has no override.
//
// This is what makes "which rows predate the current policy" answerable across
// repos that are deliberately scored under different rules: a sweep compares
// each row against its OWN repo's hash rather than against one global figure
// that its repo never used.
func (c Config) CurrentRuleHashes() map[string]string {
	out := map[string]string{"": c.ResolveScoring("").Hash()}
	for repo := range c.Scoring.Repos {
		out[repo] = c.ResolveScoring(repo).Hash()
	}
	return out
}

// ValidateScoring reports scoring misconfigurations, globally and per repo.
//
// It exists because ResolveScoring deliberately swallows a bad ruleset and
// scores at defaults instead of failing a review. That is the right call at
// review time and the wrong one as the only signal, so the same problem is
// surfaced here, through the seam doctor and boot validation share. Without
// it an inverted bucket ladder scores every PR at defaults forever and says
// nothing.
func (c Config) ValidateScoring() []string {
	var problems []string
	problems = append(problems, scoringProblems("scoring", c.Scoring)...)
	for _, repo := range sortedKeys(c.Scoring.Repos) {
		over := c.Scoring.Repos[repo]
		prefix := fmt.Sprintf("scoring.repos.%s", repo)
		if len(over.Repos) > 0 {
			problems = append(problems, prefix+".repos is nested inside a repo override and would never be read; move it to scoring.repos")
		}
		problems = append(problems, scoringProblems(prefix, mergeScoring(c.Scoring, over))...)
	}
	return problems
}

func scoringProblems(prefix string, s ScoringSettings) []string {
	var problems []string
	if s.Mode != "" && !ValidScoringMode(s.Mode) {
		problems = append(problems, fmt.Sprintf("%s.mode is %q; valid: %s (reading it as %q meanwhile)",
			prefix, s.Mode, strings.Join(ScoringModes, ", "), ScoringEnabled))
	}
	if err := applyScoring(score.DefaultRules(), s).Validate(); err != nil {
		problems = append(problems, fmt.Sprintf("%s: %v", prefix, err))
	}
	return problems
}
