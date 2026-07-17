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
	// CHANGES_REQUESTED, REVIEW_REQUIRED, or empty); unlike the raw reviews
	// list, it accounts for stale/dismissed approvals.
	ReviewDecision string `json:"reviewDecision"`
	// State (OPEN | CLOSED | MERGED) is only populated by `gh pr view`; the
	// list path filters to open PRs at the query.
	State string `json:"state"`
}

type ghActor struct {
	Login string `json:"login"`
}

type ghReview struct {
	State string `json:"state"`
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

// StillCandidate re-fetches one PR and reports whether it would still pass
// the candidacy gates (open, not draft, review requested, not approved). The
// scheduler calls this just before spending an engine invocation on a
// DISCOVERED candidate; the window between discovery and review is long
// enough for someone else to have approved, merged, or closed the PR.
func StillCandidate(ctx context.Context, repo string, number int) (bool, string, error) {
	out, err := runGH(ctx, "pr", "view", fmt.Sprintf("%d", number),
		"--repo", repo,
		"--json", "number,isDraft,state,reviewRequests,reviewDecision")
	if err != nil {
		return false, "", err
	}
	return stillCandidateFromJSON(out)
}

// stillCandidateFromJSON applies the live-state gate plus the shared
// candidacy gates to a `gh pr view` payload. Pure: the state and gate
// branches are table-tested from canned JSON, mirroring candidateFromView.
func stillCandidateFromJSON(out []byte) (bool, string, error) {
	var pr ghPR
	if err := json.Unmarshal(out, &pr); err != nil {
		return false, "", fmt.Errorf("parse gh pr view: %w", err)
	}
	if pr.State != "OPEN" {
		return false, strings.ToLower(pr.State), nil
	}
	ok, reason := candidacyGate(pr)
	return ok, reason, nil
}

// ManualCandidate fetches a PR's live metadata and shapes it as a queued
// candidate: the manual-add path for both the CLI and the dashboard. Closed
// or merged PRs are rejected: there is nothing left to review. The fetch
// exists so manual adds carry title/author/SHA immediately instead of
// waiting on (and possibly never matching) discovery.
func ManualCandidate(ctx context.Context, repo string, number int) (store.Candidate, error) {
	out, err := runGH(ctx, "pr", "view", fmt.Sprintf("%d", number),
		"--repo", repo,
		"--json", "title,author,url,headRefOid,state,createdAt,updatedAt",
	)
	if err != nil {
		return store.Candidate{}, err
	}
	var pr ghPR
	if err := json.Unmarshal(out, &pr); err != nil {
		return store.Candidate{}, fmt.Errorf("parse gh pr view: %w", err)
	}
	return candidateFromView(repo, number, pr)
}

// candidateFromView applies the manual-add gate (open PRs only) and shapes a
// `gh pr view` payload — the same ghPR wire shape every other gh read uses —
// as a queued candidate. Pure: the state gate and field mapping are
// unit-tested from canned JSON.
func candidateFromView(repo string, number int, pr ghPR) (store.Candidate, error) {
	if pr.State != "OPEN" {
		return store.Candidate{}, fmt.Errorf("PR %s#%d is %s: only open PRs can be queued", repo, number, pr.State)
	}
	return store.Candidate{
		Repo:         repo,
		Number:       number,
		Type:         store.TypeNew,
		Title:        pr.Title,
		Author:       pr.Author.Login,
		URL:          pr.URL,
		HeadSHA:      pr.HeadRefOID,
		CreatedAt:    pr.CreatedAt,
		UpdatedAt:    pr.UpdatedAt,
		DiscoveredAt: time.Now(),
		Source:       store.SourceManual,
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

// isApproved reports whether GitHub's computed review decision is APPROVED:
// an approved PR is already unblocked, so there's nothing for this tool to do.
// Deliberately NOT derived from the raw reviews list: a past approval made
// stale by new commits must not block a Refreshed re-review.
func (p ghPR) isApproved() bool { return p.ReviewDecision == "APPROVED" }
