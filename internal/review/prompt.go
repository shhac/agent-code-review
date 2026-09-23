package review

import (
	"os"
	"strconv"
	"strings"

	"github.com/shhac/crew-code-review/internal/config"
	"github.com/shhac/crew-code-review/internal/store"
)

// Facts are the deterministic things the Go side knows about a candidate before
// the engine runs. Rules match on these.
type Facts struct {
	AuthorIsGHUser bool
	// Policy is the author's resolved treatment for this repo: their group,
	// what we may do with the PR, and any cohort or per-author instruction.
	// Nothing here knows that groups, overrides, and an unlisted fallback
	// produced it; the cascade resolved before this point.
	Policy config.Policy
	// Steering is the instruction the PR's author (or the account reviews are
	// posted as) attached to this PR, nil for the overwhelming majority of
	// reviews. Taken from the candidate, which carries it, so BuildPrompt
	// stays pure and there is no second place for it to disagree.
	Steering *store.Steering
	// SteeringRole says who set it RELATIVE TO THIS PR, which is what decides
	// how much authority the framing grants it. A handle alone does not tell
	// the model whether it is reading the PR author or the operator of the
	// reviewer itself.
	SteeringRole SteeringRole
	// SteeringNonce wraps the message in markers the author cannot predict.
	// Carried on Facts rather than generated inside BuildPrompt so that
	// BuildPrompt stays a pure function of its inputs, which is what lets the
	// whole prompt be asserted in a test.
	SteeringNonce string
}

// DeriveFacts computes the rule inputs for a candidate. ghUser is the resolved
// current gh login; policy comes from config.ResolvePolicy over the store's
// membership row, which the caller looks up, keeping this pure.
// Not pure, deliberately: the steering marker must be unpredictable, so it is
// drawn fresh here rather than derived from anything the caller can see. Only
// this one field is random; a test that needs a fixed prompt sets it after.
func DeriveFacts(c store.Candidate, ghUser string, policy config.Policy) Facts {
	nonce := ""
	if c.Steering != nil {
		nonce = steeringNonce()
	}
	return Facts{
		AuthorIsGHUser: ghUser != "" && strings.EqualFold(c.Author, ghUser),
		Policy:         policy,
		Steering:       c.Steering,
		SteeringRole:   steeringRole(c.Steering, c.Author, ghUser),
		SteeringNonce:  nonce,
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
	// The author's own instruction: their group's, plus anything a per-author
	// override added. It sits in the body, above the outcome sections, because
	// it shapes the whole review rather than one outcome.
	if p := strings.TrimSpace(f.Policy.Prompt); p != "" {
		b.WriteString("\n\n")
		b.WriteString(p)
	}
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
	// Steering goes LAST, after every configured instruction, and is fenced and
	// attributed. It is the one part of the prompt written by somebody other
	// than the operator: whoever set it proved they are the PR's author (or the
	// account reviews are posted as), which earns them influence over their own
	// review and nothing more. Framing it as a request from a named person,
	// rather than merging it into the operator's instructions, is what keeps
	// "focus on the migration" from being read the same way as "approve this".
	if f.Steering != nil {
		if msg := strings.TrimSpace(f.Steering.Message); msg != "" {
			b.WriteString("\n\n")
			b.WriteString(steeringSection(f.SteeringRole, f.Steering.SetBy, msg, f.SteeringNonce))
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
		if o.key == "approve" && !CanApprove(f) {
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
	if CanApprove(f) {
		return "Approval policy: you MAY approve this PR if the review warrants it, " +
			"or leave comments. @" + c.Author + " is an approvable author for " + c.Repo + "."
	}
	return "Approval policy: DO NOT approve this PR under any circumstances; only leave comments."
}

// CanApprove reports whether an APPROVE is possible for this candidate: only
// when the author's resolved policy permits approval AND it isn't a
// self-authored PR (you can't approve your own). The self-review veto sits
// ABOVE the policy cascade deliberately: no group, and no per-author override,
// may grant approving your own PR. It gates both the approval directive and
// whether the "If you APPROVED" outcome section is emitted at all: there's no
// point instructing the agent on an outcome it is forbidden from reaching.
func CanApprove(f Facts) bool { return f.Policy.MayApprove() && !f.AuthorIsGHUser }

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
