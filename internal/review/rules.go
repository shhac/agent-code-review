package review

import (
	"slices"
	"strings"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
)

// matches evaluates a rule condition against a candidate + facts. Unset fields
// are wildcards; every set field must hold. Outcome is deliberately not checked
// here: it routes the fragment (see outcomeInstructions), it does not gate it.
func matches(w config.Condition, c store.Candidate, f Facts) bool {
	ok, _ := matchReason(w, c, f)
	return ok
}

// matchReason is matches plus a human-readable reason for the FIRST failing
// condition (empty when it matches). It powers `prompts preview --explain` so
// authors can see exactly why a rule did or didn't fire for a given candidate.
func matchReason(w config.Condition, c store.Candidate, f Facts) (bool, string) {
	if w.AuthorIsGHUser && !f.AuthorIsGHUser {
		return false, "needs author_is_gh_user (self-authored)"
	}
	if w.AuthorNotGHUser && f.AuthorIsGHUser {
		return false, "needs author_not_gh_user (not self-authored)"
	}
	if w.AuthorAllowed && !f.Policy.MayApprove() {
		return false, "needs author_allowed"
	}
	if w.AuthorNotAllowed && f.Policy.MayApprove() {
		return false, "needs author_not_allowed"
	}
	// Group names are ours, so they match exactly; handles are GitHub's, so
	// they match the way GitHub treats them.
	if len(w.Groups) > 0 && !slices.Contains(w.Groups, f.Policy.Group) {
		return false, "group " + f.Policy.Group + " not in [" + strings.Join(w.Groups, ", ") + "]"
	}
	// RepoMatches is a case-insensitive membership test; handles carry the same
	// GitHub identity semantics repos do, so it is the right check for both.
	if len(w.Authors) > 0 && !config.RepoMatches(w.Authors, c.Author) {
		return false, "author not in [" + strings.Join(w.Authors, ", ") + "]"
	}
	if w.CandidateType != "" && !strings.EqualFold(w.CandidateType, c.Type) {
		return false, "needs candidate_type=" + w.CandidateType
	}
	if len(w.Repos) > 0 && !config.RepoMatches(w.Repos, c.Repo) {
		return false, "repo not in [" + strings.Join(w.Repos, ", ") + "]"
	}
	return true, ""
}

// RuleTrace explains one rule's fate for a candidate: whether it fired, where
// its fragment lands (the prompt body, or a named outcome section), and — when
// skipped — why. An outcome-scoped rule that Matched still only reaches the
// agent if the agent lands on that outcome; Target names which one.
type RuleTrace struct {
	Name    string `json:"name"`
	Target  string `json:"target"` // "body" or "approve" | "comment" | "reject"
	Matched bool   `json:"matched"`
	Reason  string `json:"reason,omitempty"`
}

// ExplainRules traces every configured rule against a candidate + facts, in
// config order, without assembling the prompt. It is the introspection behind
// the preview's --explain mode.
func ExplainRules(cfg config.Config, c store.Candidate, f Facts) []RuleTrace {
	traces := make([]RuleTrace, 0, len(cfg.Review.Rules))
	for _, rule := range cfg.Review.Rules {
		target := "body"
		if rule.When.Outcome != "" {
			target = strings.ToLower(rule.When.Outcome)
		}
		ok, reason := matchReason(rule.When, c, f)
		traces = append(traces, RuleTrace{Name: rule.Name, Target: target, Matched: ok, Reason: reason})
	}
	return traces
}
