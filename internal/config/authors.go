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
	"strconv"
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
	p, _ := c.resolve(repo, handle, m, false)
	return p
}

// ExplainPolicy is ResolvePolicy plus the layer that decided each field, in
// the order the layers applied. It powers the preview's --explain mode and
// `authors ls`.
func (c Config) ExplainPolicy(repo, handle string, m Membership) (Policy, []PolicyStep) {
	return c.resolve(repo, handle, m, true)
}

func (c Config) resolve(repo, handle string, m Membership, explain bool) (Policy, []PolicyStep) {
	var trace []PolicyStep
	record := func(field, value, source string) {
		if explain {
			trace = append(trace, PolicyStep{Field: field, Value: value, Source: source})
		}
	}

	name, source := c.groupFor(repo, m)
	record("group", name, source)

	p := Policy{Group: name}
	if g, ok := c.Group(name); ok {
		p.patch(g, "group["+name+"]", record)
	}
	for _, o := range c.Authors.Overrides {
		if o.matches(repo, handle) {
			p.patch(o.Group, "override["+o.Handle+"]", record)
		}
	}
	// No layer stated a level: either the group omitted one, or it names a
	// group that does not exist (a stale membership row, or a group deleted
	// from config). Comment is the safe landing: we still review, so the work
	// is not silently dropped, but we cannot approve on a policy nobody wrote.
	if p.Review == "" {
		p.Review = ReviewComment
		record("review", ReviewComment, "default (no group set one)")
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

// GroupNames lists every group that can be referenced, declared and built-in,
// sorted: the source for shell completion and the CLI's error text.
func (c Config) GroupNames() []string {
	seen := map[string]bool{}
	names := make([]string, 0, len(c.Authors.Groups)+len(builtinGroups))
	for name := range c.Authors.Groups {
		seen[name] = true
		names = append(names, name)
	}
	for _, name := range BuiltinGroups() {
		if !seen[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// patch layers one set of fields onto the policy, recording every field it
// decides. Scalars are last-writer-wins; prompts accumulate, so an override
// adds a personal line to its group's instruction rather than silently
// dropping it. That also matches how rule fragments already concatenate.
func (p *Policy) patch(g Group, source string, record func(field, value, source string)) {
	assign(&p.Review, g.Review, "review", source, record)
	assign(&p.Engine, g.Engine, "engine", source, record)
	assign(&p.Model, g.Model, "model", source, record)
	assign(&p.Effort, g.Effort, "effort", source, record)
	if fragment := strings.TrimSpace(g.Prompt); fragment != "" {
		if p.Prompt != "" {
			p.Prompt += "\n\n"
		}
		p.Prompt += fragment
		record("prompt", fragment, source)
	}
}

func assign(dst *string, value, field, source string, record func(field, value, source string)) {
	if value == "" {
		return
	}
	*dst = value
	record(field, value, source)
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
// does not.
func RepoScopeMatches(scope []string, repo string) bool {
	if len(scope) == 0 {
		return true
	}
	for _, r := range scope {
		if r == WildcardRepo || strings.EqualFold(r, repo) {
			return true
		}
	}
	return false
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
	for _, name := range c.GroupNames() {
		if g, ok := c.Group(name); ok {
			add(g.Engine)
		}
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
	for _, name := range c.GroupNames() {
		if g, ok := c.Group(name); ok && g.Engine == engine {
			users = append(users, "group "+name)
		}
	}
	for _, o := range c.Authors.Overrides {
		if o.Engine == engine {
			users = append(users, "override "+o.Handle)
		}
	}
	return users
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
			problems = append(problems, "authors.groups."+name+".review is "+quoted(g.Review)+
				"; valid: "+strings.Join(ReviewLevels, ", "))
		}
		problems = append(problems, engineProblem("authors.groups."+name, g.Engine)...)
	}
	for _, repo := range sortedKeys(c.Authors.Unlisted) {
		if repo != WildcardRepo && !ValidRepoName(repo) {
			problems = append(problems, "authors.unlisted key "+quoted(repo)+
				` is not a repo; use "owner/name" or "*"`)
		}
		if group := c.Authors.Unlisted[repo]; group != "" {
			if _, ok := c.Group(group); !ok {
				problems = append(problems, "authors.unlisted["+repo+"] names group "+quoted(group)+
					", which is not defined; valid: "+strings.Join(c.GroupNames(), ", "))
			}
		}
	}
	for i, o := range c.Authors.Overrides {
		where := "authors.overrides[" + strconv.Itoa(i) + "]"
		if strings.TrimSpace(o.Handle) == "" {
			problems = append(problems, where+" has no handle, so it can never match an author")
		}
		if o.Review != "" && !ValidReviewLevel(o.Review) {
			problems = append(problems, where+".review is "+quoted(o.Review)+
				"; valid: "+strings.Join(ReviewLevels, ", "))
		}
		problems = append(problems, engineProblem(where, o.Engine)...)
		for _, repo := range o.Repos {
			if repo != WildcardRepo && !ValidRepoName(repo) {
				problems = append(problems, where+".repos entry "+quoted(repo)+
					` is not a repo; use "owner/name" or "*"`)
			}
		}
	}
	return problems
}

func engineProblem(where, engine string) []string {
	if engine == "" || slices.Contains(EngineNames, engine) {
		return nil
	}
	return []string{where + ".engine is " + quoted(engine) + "; valid: " + strings.Join(EngineNames, ", ")}
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func quoted(s string) string { return `"` + s + `"` }
