package discover

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ghPR is the subset of `gh pr list --json ...` we consume.
type ghPR struct {
	Number     int       `json:"number"`
	Title      string    `json:"title"`
	Author     ghActor   `json:"author"`
	HeadRefOID string    `json:"headRefOid"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
	IsDraft    bool      `json:"isDraft"`
	URL        string    `json:"url"`
	// reviewRequests items are users ({login}) or teams ({name}); we only
	// need to know whether any request is outstanding.
	ReviewRequests []ghActor  `json:"reviewRequests"`
	Reviews        []ghReview `json:"reviews"`
}

type ghActor struct {
	Login string `json:"login"`
	Name  string `json:"name"`
}

type ghReview struct {
	Author ghActor `json:"author"`
	State  string  `json:"state"`
}

// prListFields is the JSON field set requested from `gh pr list`.
const prListFields = "number,title,author,headRefOid,createdAt,updatedAt,isDraft,url,reviewRequests,reviews"

// runGH executes the gh CLI and returns stdout, surfacing stderr on failure.
func runGH(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("gh %s: %s", strings.Join(args, " "), msg)
	}
	return out, nil
}

// CurrentUser returns the authenticated gh login (`gh api user`).
func CurrentUser(ctx context.Context) (string, error) {
	out, err := runGH(ctx, "api", "user", "--jq", ".login")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// hasOpenReviewRequest reports whether any review is currently requested.
func (p ghPR) hasOpenReviewRequest() bool { return len(p.ReviewRequests) > 0 }

// hasAnyReview reports whether anyone has ever reviewed this PR.
func (p ghPR) hasAnyReview() bool {
	for _, r := range p.Reviews {
		// GitHub check annotations don't appear here; any entry is a human/bot review.
		if r.State != "" {
			return true
		}
	}
	return false
}
