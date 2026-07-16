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
		// Outcome-scoped rules render under their bullet (outcomeInstructions);
		// only unscoped rules append to the body here.
		if rule.When.Outcome == "" && matches(rule.When, c, f) {
			b.WriteString("\n\n")
			b.WriteString(strings.TrimSpace(rule.Prompt))
		}
	}
	return strings.TrimSpace(b.String())
}

// outcomeInstructions renders the configured post-outcome fragments. Each
// bullet is the base slot (on_approve / on_comment / on_reject) followed by any
// outcome-scoped rule whose condition matches this candidate, so allow-list
// (or repo / type) awareness is decided deterministically here, not by prompt
// phrasing. A bullet appears only when it has content; when none do, the whole
// section is omitted. The content is the user's own (team conventions, their
// tooling); the tool just routes it to the right outcome.
func outcomeInstructions(r config.ReviewSettings, c store.Candidate, f Facts) string {
	type outcome struct{ key, label, base string }
	outcomes := []outcome{
		{"approve", "If you APPROVED this PR", r.OnApprove},
		{"comment", "If you COMMENTED without approving", r.OnComment},
		{"reject", "If you REQUESTED CHANGES (rejected)", r.OnReject},
	}
	var lines []string
	for _, o := range outcomes {
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
			lines = append(lines, "- "+o.label+": "+strings.Join(parts, " "))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "After completing the review, follow the instruction matching your outcome:\n" +
		strings.Join(lines, "\n")
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
	if f.AuthorAllowed && !f.AuthorIsGHUser {
		return "Approval policy: you MAY approve this PR if the review warrants it, " +
			"or leave comments. @" + c.Author + " is an allowed author for " + c.Repo + "."
	}
	return "Approval policy: DO NOT approve this PR under any circumstances; only leave comments."
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
	if w.AuthorIsGHUser && !f.AuthorIsGHUser {
		return false
	}
	if w.AuthorAllowed && !f.AuthorAllowed {
		return false
	}
	if w.AuthorNotAllowed && f.AuthorAllowed {
		return false
	}
	if w.CandidateType != "" && !strings.EqualFold(w.CandidateType, c.Type) {
		return false
	}
	if len(w.Repos) > 0 && !config.RepoMatches(w.Repos, c.Repo) {
		return false
	}
	return true
}
