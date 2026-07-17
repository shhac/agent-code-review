package review

import (
	"os"
	"strconv"
	"strings"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
)

// Facts are the deterministic things the Go side knows about a candidate before
// the engine runs. Rules match on these.
type Facts struct {
	AuthorIsGHUser bool
	AuthorAllowed  bool // author is on the allowed-authors list for this repo
}

// DeriveFacts computes the rule inputs for a candidate. ghUser is the resolved
// current gh login; authorAllowed comes from the store's per-repo allowed-authors
// list (see store.IsAuthorAllowed); the caller looks it up, keeping this pure.
func DeriveFacts(c store.Candidate, ghUser string, authorAllowed bool) Facts {
	return Facts{
		AuthorIsGHUser: ghUser != "" && strings.EqualFold(c.Author, ghUser),
		AuthorAllowed:  authorAllowed,
	}
}

// BuildPrompt assembles the engine instructions: the main prompt, then every
// matching rule's fragment, in config order. This is where self-review and
// non-allow-list authors get their comment-only instruction, and where the
// post-approve Slack behavior is injected: all as prompt, never Go control flow.
func BuildPrompt(cfg config.Config, c store.Candidate, f Facts) string {
	var b strings.Builder
	b.WriteString(MainPrompt(cfg.Review))
	b.WriteString("\n\n")
	b.WriteString(candidateContext(c))
	b.WriteString("\n")
	b.WriteString(approvalDirective(c, f))
	if outcome := outcomeInstructions(cfg.Review, c, f); outcome != "" {
		b.WriteString("\n\n")
		b.WriteString(outcome)
	}
	for _, rule := range cfg.Review.Rules {
		// Outcome-scoped rules render under their section (outcomeInstructions);
		// only unscoped rules append to the body here.
		if rule.When.Outcome == "" && matches(rule.When, c, f) {
			b.WriteString("\n\n")
			b.WriteString(strings.TrimSpace(rule.Prompt))
		}
	}
	return strings.TrimSpace(b.String())
}

// outcomeInstructions renders the configured post-outcome fragments as one
// markdown section per outcome: a `## <label>` heading followed by the base
// slot (on_approve / on_comment / on_reject) and any outcome-scoped rule whose
// condition matches this candidate. Allow-list (or repo / type) awareness is
// decided deterministically here, not by prompt phrasing. Headings (not inline
// bullets) so a multiline slot value keeps its own indentation, sub-lists, and
// code blocks verbatim, and base + rules read as separate blocks. A section
// appears only when it has content AND the outcome is reachable (the approve
// section is omitted when approval is forbidden); when none do, the whole block
// is omitted.
// The content is the user's own (team conventions, their tooling); the tool
// just routes it to the right outcome.
func outcomeInstructions(r config.ReviewSettings, c store.Candidate, f Facts) string {
	type outcome struct{ key, label, base string }
	outcomes := []outcome{
		{"approve", "If you APPROVED this PR", r.OnApprove},
		{"comment", "If you COMMENTED without approving", r.OnComment},
		{"reject", "If you REQUESTED CHANGES (rejected)", r.OnReject},
	}
	var sections []string
	for _, o := range outcomes {
		// Skip the approve section when approval is impossible (author not on the
		// allow-list, or self-authored): it would be an unreachable, contradictory
		// instruction next to the "DO NOT approve" directive.
		if o.key == "approve" && !canApprove(f) {
			continue
		}
		var parts []string
		if base := strings.TrimSpace(o.base); base != "" {
			parts = append(parts, base)
		}
		for _, rule := range r.Rules {
			if strings.EqualFold(rule.When.Outcome, o.key) && matches(rule.When, c, f) {
				if p := strings.TrimSpace(rule.Prompt); p != "" {
					parts = append(parts, p)
				}
			}
		}
		if len(parts) > 0 {
			sections = append(sections, "## "+o.label+"\n"+strings.Join(parts, "\n\n"))
		}
	}
	if len(sections) == 0 {
		return ""
	}
	return "After completing the review, follow the instruction that matches your outcome.\n\n" +
		strings.Join(sections, "\n\n")
}

// MainPrompt resolves the main review prompt: main_prompt_path wins when set
// and readable, else the inline main_prompt. Exported for the dashboard's
// read-only prompt view.
func MainPrompt(r config.ReviewSettings) string {
	if r.MainPromptPath != "" {
		if data, err := os.ReadFile(r.MainPromptPath); err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	return strings.TrimSpace(r.MainPrompt)
}

// defaultResumePrompt nudges a session that yielded its turn on a WORKING
// report to pick the review back up. resume_prompt in config overrides it.
const defaultResumePrompt = "Your last message was an intermediate WORKING update, but you stopped there " +
	"without finishing the review. Keep going until you arrive at a decision: continue from where you " +
	"left off, complete every remaining required action, and only stop once your FINAL message reports " +
	"the real outcome (APPROVED, COMMENTED, REQUESTED_CHANGES, or SKIPPED) per the schema. Never end on WORKING."

// ResumePrompt resolves the nudge sent when resuming a run that ended on a
// WORKING report: the configured resume_prompt, else the built-in default.
// Exported for the prompts CLI's show view.
func ResumePrompt(r config.ReviewSettings) string {
	if p := strings.TrimSpace(r.ResumePrompt); p != "" {
		return p
	}
	return defaultResumePrompt
}

// approvalDirective states the approval policy for THIS PR as a hard
// instruction, so comment-only is the default and an APPROVE is only ever
// permitted when explicitly allowed, never as a fallback when a rule is
// missing. Approval is allowed only when the author is on the allowed-authors
// list for this repo AND it isn't a self-authored PR (you can't approve your
// own PR).
//
// The negative case gives no reason: revealing "this is self-authored" would
// leak the current gh user's identity, which the spec forbids. Only the single
// author↔allowed pair for this PR is ever exposed, never the whole list.
func approvalDirective(c store.Candidate, f Facts) string {
	if canApprove(f) {
		return "Approval policy: you MAY approve this PR if the review warrants it, " +
			"or leave comments. @" + c.Author + " is an allowed author for " + c.Repo + "."
	}
	return "Approval policy: DO NOT approve this PR under any circumstances; only leave comments."
}

// canApprove reports whether an APPROVE is possible for this candidate: only
// when the author is on the allowed-authors list AND it isn't a self-authored
// PR (you can't approve your own). It gates both the approval directive and
// whether the "If you APPROVED" outcome section is emitted at all — there's no
// point instructing the agent on an outcome it is forbidden from reaching.
func canApprove(f Facts) bool { return f.AuthorAllowed && !f.AuthorIsGHUser }

// SampleRepo is the placeholder repo for synthetic prompt previews. The CLI's
// `prompts preview` and the dashboard both render this fixture, so it lives in
// one place to keep them identical.
const SampleRepo = "example-org/example-repo"

// SampleCandidate builds the synthetic candidate used for prompt previews from
// an already-defaulted, already-validated repo and candidate type. Both preview
// paths assemble from this one fixture so they can't drift.
func SampleCandidate(repo, candidateType string) store.Candidate {
	return store.Candidate{
		Repo:    repo,
		Number:  123,
		Type:    candidateType,
		Author:  "example-author",
		URL:     "https://github.com/" + repo + "/pull/123",
		HeadSHA: "0000000000000000000000000000000000000000",
	}
}

func candidateContext(c store.Candidate) string {
	var b strings.Builder
	b.WriteString("Review this pull request:\n")
	b.WriteString("- Repo: " + c.Repo + "\n")
	b.WriteString("- PR: #" + strconv.Itoa(c.Number) + "\n")
	b.WriteString("- URL: " + c.URL + "\n")
	b.WriteString("- Type: " + c.Type + "\n")
	b.WriteString("- Head SHA: " + c.HeadSHA)
	return b.String()
}

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
	if w.AuthorAllowed && !f.AuthorAllowed {
		return false, "needs author_allowed"
	}
	if w.AuthorNotAllowed && f.AuthorAllowed {
		return false, "needs author_not_allowed"
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
