package dashboard

import (
	"net/http"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
)

type promptOutcomesResp struct {
	OnApprove string `json:"on_approve"`
	OnComment string `json:"on_comment"`
	OnReject  string `json:"on_reject"`
}

type promptPreviewsResp struct {
	AllowedAuthor    string `json:"allowed_author"`
	NotAllowedAuthor string `json:"not_allowed_author"`
}

type promptResp struct {
	MainPrompt string             `json:"main_prompt"`
	Outcomes   promptOutcomesResp `json:"outcomes"`
	Rules      []config.Rule      `json:"rules"`
	Previews   promptPreviewsResp `json:"previews"`
	Note       string             `json:"note"`
}

// handlePrompt exposes the review prompt read-only: the main prompt, the rule
// fragments, and two fully assembled previews (allowed vs not-allowed author)
// built from a synthetic candidate so you can see exactly what the agent gets.
// The engine driver appends its own reporting instruction on top of this.
func (s *Server) handlePrompt(w http.ResponseWriter, _ *http.Request) {
	cfg := s.config()
	sample := store.Candidate{
		Repo:    "example-org/example-repo",
		Number:  123,
		Type:    store.TypeNew,
		Author:  "example-author",
		URL:     "https://github.com/example-org/example-repo/pull/123",
		HeadSHA: "0000000000000000000000000000000000000000",
	}
	writeJSON(w, http.StatusOK, promptResp{
		MainPrompt: review.MainPrompt(cfg.Review),
		Outcomes: promptOutcomesResp{
			OnApprove: cfg.Review.OnApprove,
			OnComment: cfg.Review.OnComment,
			OnReject:  cfg.Review.OnReject,
		},
		Rules: cfg.Review.Rules,
		Previews: promptPreviewsResp{
			AllowedAuthor:    review.BuildPrompt(cfg, sample, review.Facts{AuthorAllowed: true}),
			NotAllowedAuthor: review.BuildPrompt(cfg, sample, review.Facts{}),
		},
		Note: "Previews use a synthetic candidate. The engine driver appends a reporting instruction (final message = JSON verdict) on top of this.",
	})
}
