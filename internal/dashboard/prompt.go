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
	AuthorAllowed  bool   `json:"author_allowed"`
	AuthorIsGHUser bool   `json:"author_is_gh_user"`
}

// promptPreviewResp is the fully assembled prompt for the shaped candidate plus
// a per-rule trace (what fired, where it lands, why it was skipped) — the same
// data as the CLI's `prompts preview --explain`.
type promptPreviewResp struct {
	Candidate promptPreviewCandidate `json:"candidate"`
	Preview   string                 `json:"preview"`
	Rules     []review.RuleTrace     `json:"rules"`
}

// handlePromptPreview assembles the prompt for a synthetic candidate shaped by
// query params: author_allowed (default true), author_is_gh_user (default
// false), candidate_type (default new), repo (default the example repo).
// Assembly semantics live in review.Preview; this handler is transport only.
func (s *Server) handlePromptPreview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	facts := review.Facts{
		AuthorAllowed:  q.Get("author_allowed") != "false", // default allowed
		AuthorIsGHUser: q.Get("author_is_gh_user") == "true",
	}
	res, err := review.Preview(s.config(), q.Get("repo"), q.Get("candidate_type"), facts)
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
			AuthorAllowed:  res.Facts.AuthorAllowed,
			AuthorIsGHUser: res.Facts.AuthorIsGHUser,
		},
		Preview: res.Prompt,
		Rules:   res.Rules,
	})
}
