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
// list (see store.IsAuthorAllowed) — the caller looks it up, keeping this pure.
func DeriveFacts(c store.Candidate, ghUser string, authorAllowed bool) Facts {
	return Facts{
		AuthorIsGHUser: ghUser != "" && strings.EqualFold(c.Author, ghUser),
		AuthorAllowed:  authorAllowed,
	}
}

// BuildPrompt assembles the engine instructions: the main prompt, then every
// matching rule's fragment, in config order. This is where self-review and
// non-allow-list authors get their comment-only instruction, and where the
// post-approve Slack behavior is injected — all as prompt, never Go control flow.
func BuildPrompt(cfg config.Config, c store.Candidate, f Facts) string {
	var b strings.Builder
	b.WriteString(MainPrompt(cfg.Review))
	b.WriteString("\n\n")
	b.WriteString(candidateContext(c))
	b.WriteString("\n")
	b.WriteString(approvalDirective(c, f))
	if outcome := outcomeInstructions(cfg.Review); outcome != "" {
		b.WriteString("\n\n")
		b.WriteString(outcome)
	}
	for _, rule := range cfg.Review.Rules {
		if matches(rule.When, c, f) {
			b.WriteString("\n\n")
			b.WriteString(strings.TrimSpace(rule.Prompt))
		}
	}
	return strings.TrimSpace(b.String())
}

// outcomeInstructions renders the configured post-outcome fragments. Only
// configured outcomes appear; when none are set the section is omitted
// entirely. The content is the user's own (their team conventions, their
// tooling) — the tool just routes it to the right outcome.
func outcomeInstructions(r config.ReviewSettings) string {
	type outcome struct{ label, prompt string }
	outcomes := []outcome{
		{"If you APPROVED this PR", r.OnApprove},
		{"If you COMMENTED without approving", r.OnComment},
		{"If you REQUESTED CHANGES (rejected)", r.OnReject},
	}
	var lines []string
	for _, o := range outcomes {
		if p := strings.TrimSpace(o.prompt); p != "" {
			lines = append(lines, "- "+o.label+": "+p)
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
// permitted when explicitly allowed — never as a fallback when a rule is
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
	return "Approval policy: DO NOT approve this PR under any circumstances — only leave comments."
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
// are wildcards; every set field must hold.
func matches(w config.Condition, c store.Candidate, f Facts) bool {
	if w.AuthorIsGHUser && !f.AuthorIsGHUser {
		return false
	}
	if w.AuthorNotAllowed && f.AuthorAllowed {
		return false
	}
	if w.CandidateType != "" && !strings.EqualFold(w.CandidateType, c.Type) {
		return false
	}
	if len(w.Repos) > 0 && !contains(w.Repos, c.Repo) {
		return false
	}
	return true
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
