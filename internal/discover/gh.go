package discover

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/shhac/agent-code-review/internal/store"
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
	// ReviewDecision is GitHub's computed current state (APPROVED,
	// CHANGES_REQUESTED, REVIEW_REQUIRED, or empty) — unlike the raw reviews
	// list, it accounts for stale/dismissed approvals.
	ReviewDecision string `json:"reviewDecision"`
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
const prListFields = "number,title,author,headRefOid,createdAt,updatedAt,isDraft,url,reviewRequests,reviews,reviewDecision"

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

// PRMetadata is the manual-add view of a PR (`gh pr view`).
type PRMetadata struct {
	Title     string
	Author    string
	URL       string
	HeadSHA   string
	State     string // OPEN | CLOSED | MERGED
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ManualCandidate fetches a PR's live metadata and shapes it as a queued
// candidate — the manual-add path for both the CLI and the dashboard. Closed
// or merged PRs are rejected: there is nothing left to review.
func ManualCandidate(ctx context.Context, repo string, number int) (store.Candidate, error) {
	meta, err := FetchPR(ctx, repo, number)
	if err != nil {
		return store.Candidate{}, err
	}
	return candidateFromMetadata(repo, number, meta)
}

// candidateFromMetadata applies the manual-add gate (open PRs only) and shapes
// the metadata as a queued candidate. Pure — the state gate and field mapping
// are unit-tested without gh.
func candidateFromMetadata(repo string, number int, meta PRMetadata) (store.Candidate, error) {
	if meta.State != "OPEN" {
		return store.Candidate{}, fmt.Errorf("PR %s#%d is %s — only open PRs can be queued", repo, number, meta.State)
	}
	return store.Candidate{
		Repo:         repo,
		Number:       number,
		Type:         store.TypeNew,
		Title:        meta.Title,
		Author:       meta.Author,
		URL:          meta.URL,
		HeadSHA:      meta.HeadSHA,
		CreatedAt:    meta.CreatedAt,
		UpdatedAt:    meta.UpdatedAt,
		DiscoveredAt: time.Now(),
	}, nil
}

// FetchPR fetches one PR's metadata so manual adds carry title/author/SHA
// immediately instead of waiting on (and possibly never matching) discovery.
func FetchPR(ctx context.Context, repo string, number int) (PRMetadata, error) {
	out, err := runGH(ctx, "pr", "view", fmt.Sprintf("%d", number),
		"--repo", repo,
		"--json", "title,author,url,headRefOid,state,createdAt,updatedAt",
	)
	if err != nil {
		return PRMetadata{}, err
	}
	return parsePRMetadata(out)
}

// parsePRMetadata maps `gh pr view --json` output to PRMetadata. Pure — the
// field mapping is unit-tested from canned JSON.
func parsePRMetadata(out []byte) (PRMetadata, error) {
	var raw struct {
		Title      string    `json:"title"`
		Author     ghActor   `json:"author"`
		URL        string    `json:"url"`
		HeadRefOID string    `json:"headRefOid"`
		State      string    `json:"state"`
		CreatedAt  time.Time `json:"createdAt"`
		UpdatedAt  time.Time `json:"updatedAt"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return PRMetadata{}, err
	}
	return PRMetadata{
		Title:     raw.Title,
		Author:    raw.Author.Login,
		URL:       raw.URL,
		HeadSHA:   raw.HeadRefOID,
		State:     raw.State,
		CreatedAt: raw.CreatedAt,
		UpdatedAt: raw.UpdatedAt,
	}, nil
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

// isApproved reports whether GitHub's computed review decision is APPROVED —
// an approved PR is already unblocked, so there's nothing for this tool to do.
// Deliberately NOT derived from the raw reviews list: a past approval made
// stale by new commits must not block a Refreshed re-review.
func (p ghPR) isApproved() bool { return p.ReviewDecision == "APPROVED" }
