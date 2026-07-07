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
	AuthorIsGHUser   bool
	AuthorApprovable bool // author is on the approver allow-list for this repo
}

// DeriveFacts computes the rule inputs for a candidate. ghUser is the resolved
// current gh login; authorApprovable comes from the store's per-repo approver
// list (see store.IsApprover) — the caller looks it up, keeping this pure.
func DeriveFacts(c store.Candidate, ghUser string, authorApprovable bool) Facts {
	return Facts{
		AuthorIsGHUser:   ghUser != "" && strings.EqualFold(c.Author, ghUser),
		AuthorApprovable: authorApprovable,
	}
}

// BuildPrompt assembles the engine instructions: the main prompt, then every
// matching rule's fragment, in config order. This is where self-review and
// non-allow-list authors get their comment-only instruction, and where the
// post-approve Slack behavior is injected — all as prompt, never Go control flow.
func BuildPrompt(cfg config.Config, c store.Candidate, f Facts) string {
	var b strings.Builder
	b.WriteString(mainPrompt(cfg.Review))
	b.WriteString("\n\n")
	b.WriteString(candidateContext(c))
	b.WriteString("\n")
	b.WriteString(approverLine(c, f))
	for _, rule := range cfg.Review.Rules {
		if matches(rule.When, c, f) {
			b.WriteString("\n\n")
			b.WriteString(strings.TrimSpace(rule.Prompt))
		}
	}
	return strings.TrimSpace(b.String())
}

func mainPrompt(r config.ReviewSettings) string {
	if r.MainPromptPath != "" {
		if data, err := os.ReadFile(r.MainPromptPath); err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	return strings.TrimSpace(r.MainPrompt)
}

// approverLine passes only the specific author↔approvable pair for THIS PR into
// the prompt — never the whole allow-list. The engine uses it to decide whether
// an APPROVE is permitted for this author on this repo.
func approverLine(c store.Candidate, f Facts) string {
	verb := "is NOT on"
	if f.AuthorApprovable {
		verb = "is on"
	}
	return "Approver status: @" + c.Author + " " + verb + " the approver allow-list for " + c.Repo + "."
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
	if w.AuthorNotInAllowlist && f.AuthorApprovable {
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
