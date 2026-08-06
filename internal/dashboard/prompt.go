package dashboard

import (
	"errors"
	"net/http"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/review"
)

type promptOutcomesResp struct {
	OnApprove string `json:"on_approve"`
	OnComment string `json:"on_comment"`
	OnReject  string `json:"on_reject"`
}

type promptResp struct {
	MainPrompt string             `json:"main_prompt"`
	Outcomes   promptOutcomesResp `json:"outcomes"`
	Rules      []config.Rule      `json:"rules"`
	Repos      []string           `json:"repos"` // watched repos, for the preview repo picker
	Note       string             `json:"note"`
}

// handlePrompt exposes the review prompt read-only: the main prompt, the
// post-outcome slots, and the rule fragments. The assembled preview itself is
// served by handlePromptPreview, which takes candidate facts as query params so
// the UI can toggle allow-list / self-authorship / candidate type / repo.
func (s *Server) handlePrompt(w http.ResponseWriter, _ *http.Request) {
	cfg := s.config()
	writeJSON(w, http.StatusOK, promptResp{
		MainPrompt: review.MainPrompt(cfg.Review),
		Outcomes: promptOutcomesResp{
			OnApprove: cfg.Review.OnApprove,
			OnComment: cfg.Review.OnComment,
			OnReject:  cfg.Review.OnReject,
		},
		Rules: cfg.Review.Rules,
		Repos: cfg.SortedRepos(),
		Note:  "Previews use a synthetic candidate. The engine driver appends a reporting instruction (final message = JSON verdict) on top of this.",
	})
}

type promptPreviewCandidate struct {
	Repo           string `json:"repo"`
	CandidateType  string `json:"candidate_type"`
	Author         string `json:"author"`
	Group          string `json:"group"`
	AuthorAllowed  bool   `json:"author_allowed"`
	AuthorIsGHUser bool   `json:"author_is_gh_user"`
}

// promptPreviewResp is the fully assembled prompt for the shaped candidate plus
// two traces: the layer that decided each field of the author's policy, and
// each rule's fate. The same data as the CLI's `prompts preview --explain`.
type promptPreviewResp struct {
	Candidate promptPreviewCandidate `json:"candidate"`
	Preview   string                 `json:"preview"`
	Policy    []config.PolicyStep    `json:"policy"`
	Rules     []review.RuleTrace     `json:"rules"`
}

// handlePromptPreview assembles the prompt for a synthetic candidate shaped by
// query params: repo (default the example repo), candidate_type (default new),
// author (default the sample handle), group (the membership to simulate), and
// author_is_gh_user (default false). author_allowed predates groups and still
// works: it picks the built-in approver or commenter group.
//
// The group is SIMULATED rather than read from the roster, because this
// endpoint answers "what would a member of group X get" without needing anyone
// to actually be in it. Naming a real author still matters: per-author
// overrides key on the handle, so only a named author sees theirs fire.
// Assembly semantics live in review.Preview; this handler is transport only.
func (s *Server) handlePromptPreview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	membership := config.Membership{Group: q.Get("group"), Repo: config.WildcardRepo}
	if membership.Group == "" {
		membership.Group = config.GroupApprover
		if q.Get("author_allowed") == "false" {
			membership.Group = config.GroupCommenter
		}
	}
	res, err := review.Preview(s.config(), q.Get("repo"), q.Get("candidate_type"),
		q.Get("author"), membership, q.Get("author_is_gh_user") == "true")
	switch {
	case errors.Is(err, review.ErrBadCandidateType):
		httpError(w, http.StatusBadRequest, "candidate_type must be new or refreshed")
		return
	case errors.Is(err, review.ErrBadRepo):
		httpError(w, http.StatusBadRequest, "repo must be owner/name")
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, promptPreviewResp{
		Candidate: promptPreviewCandidate{
			Repo:           res.Repo,
			CandidateType:  res.CandidateType,
			Author:         res.Author,
			Group:          res.Facts.Policy.Group,
			AuthorAllowed:  res.Facts.Policy.MayApprove(),
			AuthorIsGHUser: res.Facts.AuthorIsGHUser,
		},
		Preview: res.Prompt,
		Policy:  res.Policy,
		Rules:   res.Rules,
	})
}
