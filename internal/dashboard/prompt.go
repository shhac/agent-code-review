package dashboard

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/review"
)

type promptOutcomesResp struct {
	OnApprove string `json:"on_approve"`
	OnComment string `json:"on_comment"`
	OnReject  string `json:"on_reject"`
}

// promptGroup is one cohort the preview can be assembled for. The DEFINED
// groups, not the ones that happen to have members: a group with nobody in it
// yet is exactly the one you are previewing while you write its prompt.
type promptGroup struct {
	Name    string `json:"name"`
	Review  string `json:"review"`
	Builtin bool   `json:"builtin"`
}

type promptResp struct {
	MainPrompt string             `json:"main_prompt"`
	Outcomes   promptOutcomesResp `json:"outcomes"`
	Rules      []config.Rule      `json:"rules"`
	Repos      []string           `json:"repos"`  // watched repos, for the preview repo picker
	Groups     []promptGroup      `json:"groups"` // ditto, for the preview group picker
	Note       string             `json:"note"`
}

// promptGroups lists the cohorts for the picker. Review level travels with the
// name so the control can say what choosing one means without a second fetch.
func promptGroups(cfg config.Config) []promptGroup {
	cohorts := cfg.Cohorts()
	out := make([]promptGroup, 0, len(cohorts))
	for _, c := range cohorts {
		review := c.Review
		if review == "" {
			review = config.ReviewComment
		}
		out = append(out, promptGroup{Name: c.Name, Review: review, Builtin: c.Builtin})
	}
	return out
}

// handlePrompt exposes the review prompt read-only: the main prompt, the
// post-outcome slots, and the rule fragments. The assembled preview itself is
// served by handlePromptPreview, which takes candidate facts as query params so
// the UI can pick the author, the group, self-authorship, candidate type and
// repo. The repo and group lists ship here so those pickers need no second
// endpoint.
func (s *Server) handlePrompt(w http.ResponseWriter, _ *http.Request) {
	cfg := s.config()
	writeJSON(w, http.StatusOK, promptResp{
		MainPrompt: review.MainPrompt(cfg.Review),
		Outcomes: promptOutcomesResp{
			OnApprove: cfg.Review.OnApprove,
			OnComment: cfg.Review.OnComment,
			OnReject:  cfg.Review.OnReject,
		},
		Rules:  cfg.Review.Rules,
		Repos:  cfg.SortedRepos(),
		Groups: promptGroups(cfg),
		Note:   "Previews use a synthetic candidate. The engine driver appends a reporting instruction (final message = JSON verdict) on top of this.",
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

// previewMembership decides which membership the preview resolves against.
//
// An explicit group SIMULATES one, which is how a cohort nobody is rostered
// into yet can still be previewed. Otherwise a named author is looked up in the
// roster, so "what does this person actually get" is answered with their real
// row. That lookup used to be missing: with no group picked the handler
// substituted the built-in approver group, so previewing a real author showed
// someone else's policy and attributed it to a group they were not in. It
// happened to agree whenever the author's real group also permitted approval,
// which is exactly the shape of bug that survives a casual look.
//
// With neither named, the answer is an author who has no roster row at all,
// which resolves through authors.unlisted.
func (s *Server) previewMembership(ctx context.Context, q url.Values) (config.Membership, error) {
	if group := q.Get("group"); group != "" {
		return config.Membership{Group: group, Repo: config.WildcardRepo}, nil
	}
	if author := q.Get("author"); author != "" {
		repo := q.Get("repo")
		if repo == "" {
			repo = review.SampleRepo
		}
		return s.store.AuthorGroup(ctx, repo, author)
	}
	// author_allowed predates groups. Honoured only when explicitly passed, so
	// an older query keeps its meaning without dictating the default.
	if allowed := q.Get("author_allowed"); allowed != "" {
		group := config.GroupApprover
		if allowed == "false" {
			group = config.GroupCommenter
		}
		return config.Membership{Group: group, Repo: config.WildcardRepo}, nil
	}
	return config.Membership{}, nil
}

// handlePromptPreview assembles the prompt for a synthetic candidate shaped by
// query params: repo (default the example repo), candidate_type (default new),
// author (default the sample handle), group (the membership to simulate), and
// author_is_gh_user (default false). author_allowed predates groups and still
// works: it picks the built-in approver or commenter group.
//
// Assembly semantics live in review.Preview; this handler is transport plus the
// one lookup below.
func (s *Server) handlePromptPreview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()

	membership, err := s.previewMembership(ctx, q)
	if err != nil {
		s.fail(w, err)
		return
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
