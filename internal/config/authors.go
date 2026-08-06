package config

// Author groups. One author resolves to exactly one group for a given repo,
// and a group is a complete review policy: what we may do with their PRs
// (ignore / comment / approve), which engine does it, and what extra
// instruction the agent gets.
//
// Resolution is two steps and pure, so it needs neither the store nor the
// network:
//
//  1. Pick the group: the author's membership row (exact repo beating the
//     wildcard row, decided by the store), else the unlisted fallback for
//     this repo, else the unlisted fallback for "*", else the legacy
//     behaviour AllowedAuthorsOnlyRepos described.
//  2. Layer the fields: group, then every matching override in config order.
//     Empty inherits; prompts accumulate.
//
// The on-disk structs live in schema.go; this file is the behaviour.

import (
	"slices"
	"sort"
	"strings"
)

// WildcardRepo matches every repo wherever a repo key is accepted: a
// membership row, an override's repo scope, and the unlisted map.
const WildcardRepo = "*"

// Review levels, ordered: each permits everything the one before it does.
// The ladder replaced two separate switches (whether we discovered an
// author's PRs at all, and whether we could approve them), which were the
// same question asked in two places.
const (
	// ReviewIgnore is never discovered. A manual `queue add` still reviews,
	// matching how manual adds already bypass every other discovery gate.
	ReviewIgnore = "ignore"
	// ReviewComment is reviewed, but can never receive an APPROVE.
	ReviewComment = "comment"
	// ReviewApprove may receive an APPROVE when the review warrants it. It is
	// still subject to the self-review veto, which no group can lift.
	ReviewApprove = "approve"
)

// ReviewLevels is the one vocabulary behind validation, the CLI's error text,
// and shell completion, ordered least to most permissive.
var ReviewLevels = []string{ReviewIgnore, ReviewComment, ReviewApprove}

// ValidReviewLevel reports whether s names a review level.
func ValidReviewLevel(s string) bool { return slices.Contains(ReviewLevels, s) }

// Built-in group names. They exist without being declared, so a fresh config
// needs no `authors.groups` block to express the pre-group behaviour, and the
// store's migration has a name to backfill legacy allow-list rows to.
// Declaring a group of the same name in config replaces the built-in.
const (
	GroupApprover  = "approver"
	GroupCommenter = "commenter"
	GroupIgnored   = "ignored"
)

var builtinGroups = map[string]Group{
	GroupApprover:  {Review: ReviewApprove},
	GroupCommenter: {Review: ReviewComment},
	GroupIgnored:   {Review: ReviewIgnore},
}

// BuiltinGroups names the groups that exist without being declared.
func BuiltinGroups() []string { return []string{GroupApprover, GroupCommenter, GroupIgnored} }

// Membership is an author's recorded group assignment, as the store resolved
// it: which group, and which repo key the row was found under (the exact repo,
// or WildcardRepo). A zero Membership means no row, which is what sends
// resolution to the unlisted fallback. The type lives in config because config
// is the layer every consumer already imports, the same reason EngineNames
// does.
type Membership struct {
	Group string `json:"group,omitempty"`
	Repo  string `json:"repo,omitempty"`
}

// Policy is one author's fully resolved treatment for one repo: the output of
// the cascade and the only thing downstream reads. Nothing below this needs to
// know that groups, overrides, or an unlisted fallback exist.
type Policy struct {
	Group  string `json:"group"`
	Review string `json:"review"`
	Engine string `json:"engine,omitempty"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	Prompt string `json:"prompt,omitempty"`
}

// MayApprove reports whether the policy permits an APPROVE. It is a necessary
// condition, not a sufficient one: the self-review veto sits above it.
func (p Policy) MayApprove() bool { return p.Review == ReviewApprove }

// Reviewable reports whether discovery should enqueue this author's PRs.
func (p Policy) Reviewable() bool { return p.Review != ReviewIgnore }

// PolicyStep records which layer decided one field. A cascade is only as
// usable as its explanation, so the trace is built by the same code that
// resolves rather than reconstructed afterwards.
type PolicyStep struct {
	Field  string `json:"field"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

// ResolvePolicy computes an author's treatment for one repo. m is the store's
// membership lookup; its zero value means the author is unlisted.
func (c Config) ResolvePolicy(repo, handle string, m Membership) Policy {
	p, _ := c.resolve(repo, handle, m)
	return p
}

// ExplainPolicy is ResolvePolicy plus the layer that decided each field, in
// the order the layers applied. It powers the preview's --explain mode and
// `authors ls`.
func (c Config) ExplainPolicy(repo, handle string, m Membership) (Policy, []PolicyStep) {
	return c.resolve(repo, handle, m)
}

// resolve runs the cascade, always building the trace. The trace is at most
// one step per field per layer, resolved once per candidate rather than in any
// hot loop, so making it conditional would buy an allocation nobody would
// notice at the cost of two code paths that have to be proven to agree.
func (c Config) resolve(repo, handle string, m Membership) (Policy, []PolicyStep) {
	name, source := c.groupFor(repo, m)
	trace := []PolicyStep{{Field: "group", Value: name, Source: source}}

	p := Policy{Group: name}
	if g, ok := c.Group(name); ok {
		trace = append(trace, p.patch(g, "group["+name+"]")...)
	}
	for _, o := range c.Authors.Overrides {
		if o.matches(repo, handle) {
			trace = append(trace, p.patch(o.Group, "override["+o.Handle+"]")...)
		}
	}
	// No layer stated a level: either the group omitted one, or it names a
	// group that does not exist (a stale membership row, or a group deleted
	// from config). Comment is the safe landing: we still review, so the work
	// is not silently dropped, but we cannot approve on a policy nobody wrote.
	if p.Review == "" {
		p.Review = ReviewComment
		trace = append(trace, PolicyStep{
			Field: "review", Value: ReviewComment, Source: "default (no group set one)"})
	}
	return p, trace
}

// UnlistedPolicy is what an author with no membership row gets on this repo:
// the question "does this repo review strangers, and how". Resolved with no
// handle, so no per-author override can match.
func (c Config) UnlistedPolicy(repo string) Policy {
	return c.ResolvePolicy(repo, "", Membership{})
}

// groupFor picks the group name and names the layer it came from.
func (c Config) groupFor(repo string, m Membership) (string, string) {
	if m.Group != "" {
		return m.Group, "membership(" + m.Repo + ")"
	}
	return c.unlistedGroup(repo)
}

// unlistedGroup is where an author with no membership row lands on this repo.
func (c Config) unlistedGroup(repo string) (string, string) {
	if group, key, ok := lookupRepo(c.Authors.Unlisted, repo); ok {
		return group, "unlisted[" + key + "]"
	}
	if group, ok := c.Authors.Unlisted[WildcardRepo]; ok {
		return group, "unlisted[*]"
	}
	// Back-compat. Before groups existed, a repo on AllowedAuthorsOnlyRepos
	// discovered nothing from an unlisted author and every other repo reviewed
	// them comment-only. Both are exactly an unlisted group, so a config that
	// has not adopted authors.unlisted keeps its prior behaviour untouched.
	if c.AuthorScopedRepo(repo) {
		return GroupIgnored, "allowed_authors_only_repos"
	}
	return GroupCommenter, "default"
}

// Group looks up a cohort definition, falling back to the built-ins. Unlike
// repos and handles, group names are matched exactly: they are our own
// identifiers rather than GitHub's, and the CLI validates them at write time,
// so an exact match is predictable instead of merely lenient.
func (c Config) Group(name string) (Group, bool) {
	if name == "" {
		return Group{}, false
	}
	if g, ok := c.Authors.Groups[name]; ok {
		return g, true
	}
	g, ok := builtinGroups[name]
	return g, ok
}

// Cohort is one referenceable group with its name attached, and whether it
// came from config or is one of the built-ins.
type Cohort struct {
	Name string
	Group
	Builtin bool
}

// Cohorts lists every group that can be referenced, declared and built-in,
// sorted by name. It is the one enumeration of "what cohorts exist": doctor's
// reachable-engine sweep, ReachableEngines, GroupsUsing, and `authors groups`
// all walk this rather than pairing GroupNames with Group themselves, so a new
// consumer cannot quietly forget the built-ins or drop the not-found case.
func (c Config) Cohorts() []Cohort {
	cohorts := make([]Cohort, 0, len(c.Authors.Groups)+len(builtinGroups))
	for name, g := range c.Authors.Groups {
		cohorts = append(cohorts, Cohort{Name: name, Group: g})
	}
	for _, name := range BuiltinGroups() {
		// A declared group of the same name replaces the built-in, so only add
		// the built-in when config did not define it.
		if _, declared := c.Authors.Groups[name]; !declared {
			cohorts = append(cohorts, Cohort{Name: name, Group: builtinGroups[name], Builtin: true})
		}
	}
	sort.Slice(cohorts, func(i, j int) bool { return cohorts[i].Name < cohorts[j].Name })
	return cohorts
}

// GroupNames is Cohorts reduced to names, for shell completion and the CLI's
// error text.
func (c Config) GroupNames() []string {
	names := make([]string, 0, len(c.Authors.Groups)+len(builtinGroups))
	for _, cohort := range c.Cohorts() {
		names = append(names, cohort.Name)
	}
	return names
}

// patch layers one set of fields onto the policy, returning the steps it
// decided. Scalars are last-writer-wins; prompts accumulate, so an override
// adds a personal line to its group's instruction rather than silently
// dropping it. That also matches how rule fragments already concatenate.
func (p *Policy) patch(g Group, source string) []PolicyStep {
	var steps []PolicyStep
	steps = setField(steps, &p.Review, g.Review, "review", source)
	steps = setField(steps, &p.Engine, g.Engine, "engine", source)
	steps = setField(steps, &p.Model, g.Model, "model", source)
	steps = setField(steps, &p.Effort, g.Effort, "effort", source)
	if fragment := strings.TrimSpace(g.Prompt); fragment != "" {
		if p.Prompt != "" {
			p.Prompt += "\n\n"
		}
		p.Prompt += fragment
		steps = append(steps, PolicyStep{Field: "prompt", Value: fragment, Source: source})
	}
	return steps
}

// setField writes value to dst and records the step that decided it. An empty
// value writes nothing, which is what makes an unset field inherit from the
// layer beneath.
func setField(steps []PolicyStep, dst *string, value, field, source string) []PolicyStep {
	if value == "" {
		return steps
	}
	*dst = value
	return append(steps, PolicyStep{Field: field, Value: value, Source: source})
}

// matches reports whether this override applies to handle on repo. Handles
// use GitHub's case-insensitive identity semantics, as everywhere else.
func (o AuthorOverride) matches(repo, handle string) bool {
	if handle == "" || !strings.EqualFold(o.Handle, handle) {
		return false
	}
	return RepoScopeMatches(o.Repos, repo)
}

// RepoScopeMatches reports whether repo is in a repo scope list, where an
// empty list and an explicit WildcardRepo entry both mean "every repo". It is
// RepoMatches for the places that accept "*", which RepoMatches deliberately
// does not, and it composes that one rather than restating the scan.
func RepoScopeMatches(scope []string, repo string) bool {
	return len(scope) == 0 || RepoMatches(scope, WildcardRepo) || RepoMatches(scope, repo)
}

// lookupRepo finds repo's entry in a repo-keyed map using GitHub's
// case-insensitive semantics, returning the value and the key it matched (for
// the trace). The wildcard key is skipped: it is the caller's explicit
// fallback, not a match for a named repo. Keys are scanned in sorted order so
// a config with two keys differing only in case resolves the same way twice.
func lookupRepo(m map[string]string, repo string) (string, string, bool) {
	if repo == "" {
		return "", "", false
	}
	if v, ok := m[repo]; ok {
		return v, repo, true
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k != WildcardRepo && strings.EqualFold(k, repo) {
			return m[k], k, true
		}
	}
	return "", "", false
}

// WithPolicy returns these settings with the policy's engine dials applied:
// what the driver for this candidate is actually built from. Only the resolved
// engine's model and effort are touched, so a policy naming claude cannot
// leave a stray model on the codex settings.
func (r ReviewSettings) WithPolicy(p Policy) ReviewSettings {
	if p.Engine != "" {
		r.Engine = p.Engine
	}
	if p.Model == "" && p.Effort == "" {
		return r
	}
	engine := r.Engine
	if engine == "" {
		engine = EngineNames[0]
	}
	model, effort := &r.Codex.Model, &r.Codex.Effort
	if engine == "claude" {
		model, effort = &r.Claude.Model, &r.Claude.Effort
	}
	if p.Model != "" {
		*model = p.Model
	}
	if p.Effort != "" {
		*effort = p.Effort
	}
	return r
}

// EngineFor is the engine that will actually review a candidate whose author
// resolved to this policy: the policy's own engine when it names one, else the
// configured default. Callers that need to ask something OF that engine (its
// usage headroom, its binary) go through here rather than checking p.Engine
// and forgetting the empty case.
func (c Config) EngineFor(p Policy) string {
	if p.Engine != "" {
		return p.Engine
	}
	return c.Engine()
}

// ReachableEngines lists every engine any candidate could actually be reviewed
// by: the configured default plus every engine a group or override names,
// deduplicated, default first. Doctor and boot validation probe this set
// rather than the configured engine alone (a typo in a rarely-used group would
// otherwise surface at 3am as an ERROR row) or every wired engine (which would
// fail a deploy over an engine nothing references).
func (c Config) ReachableEngines() []string {
	engines := []string{c.Engine()}
	add := func(name string) {
		if name != "" && !slices.Contains(engines, name) {
			engines = append(engines, name)
		}
	}
	for _, cohort := range c.Cohorts() {
		add(cohort.Engine)
	}
	for _, o := range c.Authors.Overrides {
		add(o.Engine)
	}
	return engines
}

// GroupsUsing names the groups and overrides that select engine, so a failing
// engine check can say who depends on it. The default engine is reported as
// "(default)".
func (c Config) GroupsUsing(engine string) []string {
	var users []string
	if c.Engine() == engine {
		users = append(users, "(default)")
	}
	for _, cohort := range c.Cohorts() {
		if cohort.Engine == engine {
			users = append(users, "group "+cohort.Name)
		}
	}
	for _, o := range c.Authors.Overrides {
		if o.Engine == engine {
			users = append(users, "override "+o.Handle)
		}
	}
	return users
}
