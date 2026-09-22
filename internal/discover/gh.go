package discover

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
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
	// Diff size, for author scoring. Free to ask for: `gh pr list --json` is
	// one GraphQL query whatever fields it names, and these carry no patch
	// text. They are the PR's RAW totals; the counts a score is computed from
	// have generated files taken out and are fetched per-file at claim time.
	Additions    int `json:"additions"`
	Deletions    int `json:"deletions"`
	ChangedFiles int `json:"changedFiles"`
}

type ghActor struct {
	Login string `json:"login"`
}

type ghReview struct {
	State  string  `json:"state"`
	Author ghActor `json:"author"`
	// Commit is the head the review was submitted against, which is what makes
	// "have we already reviewed THIS revision" an exact question rather than a
	// guess from timestamps.
	Commit struct {
		OID string `json:"oid"`
	} `json:"commit"`
}

// AlreadyReviewedBy reports whether login has already submitted a review at
// head. The guard against re-reviewing a PR whose previous attempt was
// interrupted AFTER it posted: that attempt recorded nothing, so nothing else
// downstream knows it happened.
//
// Until now this was prevented only as a side effect: GitHub clears the review
// request when a requested reviewer submits, so the candidacy gate happened to
// reject the PR. That is real but incidental, and it does not hold when the
// review was requested from a team, or when the request is re-added.
func (p ghPR) AlreadyReviewedBy(login, head string) bool {
	if login == "" || head == "" {
		return false
	}
	for _, r := range p.Reviews {
		if sameLogin(r.Author.Login, login) && r.Commit.OID == head {
			return true
		}
	}
	return false
}

// sameLogin compares GitHub logins the way GitHub does: case-insensitively.
// Our own login can come from the hand-typed gh_user override, so "Review-Bot"
// against GitHub's "review-bot" must still be recognised as us.
func sameLogin(a, b string) bool { return strings.EqualFold(a, b) }

// prListFields is the JSON field set requested from `gh pr list`.
const prListFields = "number,title,author,headRefOid,createdAt,updatedAt,isDraft,url,reviewRequests,reviews,reviewDecision,additions,deletions,changedFiles"

// splitRepo parses "owner/name" into its halves, which is how a GraphQL
// query names a repository. Every GraphQL read goes through ghGraphQL, so this
// is where a malformed repo is reported, in one phrasing.
func splitRepo(repo string) (owner, name string, err error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return "", "", fmt.Errorf("bad repo %q, want owner/name", repo)
	}
	return owner, name, nil
}

// ghGraphQL runs one `gh api graphql` query against repo and decodes the
// response into T. owner and repo are always bound; vars carries the rest as
// gh flag pairs ("-F", "number=7"), so each caller still says which variables
// are typed and which are strings. what names the read in a decode failure,
// which is the only way a log line can say which call GitHub answered
// strangely.
func ghGraphQL[T any](ctx context.Context, repo, what, query string, vars ...string) (T, error) {
	var resp T
	owner, name, err := splitRepo(repo)
	if err != nil {
		return resp, err
	}
	args := append([]string{"api", "graphql", "-f", "owner=" + owner, "-f", "repo=" + name}, vars...)
	raw, err := runGH(ctx, append(args, "-f", "query="+query)...)
	if err != nil {
		return resp, err
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return resp, fmt.Errorf("decode %s: %w", what, err)
	}
	return resp, nil
}

// ghPRView runs `gh pr view` for one PR, asking for exactly fields. The raw
// payload comes back so the pure halves that judge it (stillCandidateFromJSON)
// stay testable from canned JSON; decodePRView is the one way to read it.
func ghPRView(ctx context.Context, repo string, number int, fields string) ([]byte, error) {
	return runGH(ctx, "pr", "view", strconv.Itoa(number), "--repo", repo, "--json", fields)
}

// decodePRView reads a `gh pr view` payload into the shared wire shape.
func decodePRView(out []byte) (ghPR, error) {
	var pr ghPR
	if err := json.Unmarshal(out, &pr); err != nil {
		return ghPR{}, fmt.Errorf("parse gh pr view: %w", err)
	}
	return pr, nil
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
func StillCandidate(ctx context.Context, repo string, number int, requireReviewRequest bool) (bool, string, error) {
	return StillCandidateAt(ctx, repo, number, "", "", requireReviewRequest)
}

// StillCandidateAt is StillCandidate plus the already-reviewed guard: when
// login and head are supplied, a PR this login has already reviewed at that
// exact head is no longer a candidate. Used on a re-claim, where the previous
// attempt may have posted its review and then died before recording anything.
func StillCandidateAt(ctx context.Context, repo string, number int, login, head string, requireReviewRequest bool) (bool, string, error) {
	out, err := ghPRView(ctx, repo, number, "number,isDraft,state,reviewRequests,reviewDecision,reviews,headRefOid")
	if err != nil {
		return false, "", err
	}
	return stillCandidateFromJSON(out, login, head, requireReviewRequest)
}

// stillCandidateFromJSON applies the live-state gate plus the shared
// candidacy gates to a `gh pr view` payload. Pure: the state and gate
// branches are table-tested from canned JSON, mirroring candidateFromView.
func stillCandidateFromJSON(out []byte, login, head string, requireReviewRequest bool) (bool, string, error) {
	pr, err := decodePRView(out)
	if err != nil {
		return false, "", err
	}
	if pr.State != "OPEN" {
		return false, strings.ToLower(pr.State), nil
	}
	// Checked before the ordinary gates so the reason names what actually
	// happened: the work is done, not that the request went away.
	if pr.AlreadyReviewedBy(login, head) {
		return false, "already reviewed at this revision", nil
	}
	ok, reason := candidacyGate(pr, requireReviewRequest)
	return ok, reason, nil
}

// ManualCandidate fetches a PR's live metadata and shapes it as a queued
// candidate: the manual-add path for both the CLI and the dashboard. Closed
// or merged PRs are rejected: there is nothing left to review. The fetch
// exists so manual adds carry title/author/SHA immediately instead of
// waiting on (and possibly never matching) discovery.
func ManualCandidate(ctx context.Context, repo string, number int) (store.Candidate, error) {
	out, err := ghPRView(ctx, repo, number, "title,author,url,headRefOid,state,createdAt,updatedAt,additions,deletions,changedFiles")
	if err != nil {
		return store.Candidate{}, err
	}
	pr, err := decodePRView(out)
	if err != nil {
		return store.Candidate{}, err
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
	return pr.candidate(repo, number, store.TypeNew, store.SourceManual, time.Now()), nil
}

// candidate shapes a PR as a queue row. The one mapping from the gh wire shape
// to a candidate, shared by discovery and the manual add so a field added to
// one cannot be missed by the other. number is a parameter because the manual
// add's `gh pr view` does not ask for it: the caller already knows it.
func (p ghPR) candidate(repo string, number int, typ, source string, now time.Time) store.Candidate {
	return store.Candidate{
		Repo:         repo,
		Number:       number,
		Type:         typ,
		Title:        p.Title,
		Author:       p.Author.Login,
		URL:          p.URL,
		HeadSHA:      p.HeadRefOID,
		CreatedAt:    p.CreatedAt,
		UpdatedAt:    p.UpdatedAt,
		DiscoveredAt: now,
		Source:       source,
		Additions:    p.Additions,
		Deletions:    p.Deletions,
		ChangedFiles: p.ChangedFiles,
	}
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

// humanActivityQuery asks for the timestamps of everything a person can say on
// a PR: issue comments, review submissions, and inline review-thread comments.
// Bodies are deliberately not fetched. Discovery only needs to know THAT the
// conversation moved; deciding whether the move is material is the reviewing
// skill's job, and it has its own content fingerprints for that.
const humanActivityQuery = `
query($owner:String!,$repo:String!,$number:Int!){
  repository(owner:$owner,name:$repo){
    pullRequest(number:$number){
      comments(last:100){nodes{createdAt author{login __typename}}}
      reviews(last:100){nodes{createdAt author{login __typename}}}
      reviewThreads(last:100){nodes{comments(last:100){nodes{createdAt author{login __typename}}}}}
    }
  }
}`

// ghGraphQLActor is the author shape returned by humanActivityQuery. It is
// separate from ghActor because only the GraphQL surface carries __typename,
// which is the one reliable bot/human split GitHub offers: the REST and
// `gh pr view` shapes report is_bot as null for bots and humans alike.
type ghGraphQLActor struct {
	Login    string `json:"login"`
	Typename string `json:"__typename"`
}

type ghActivityNode struct {
	CreatedAt time.Time      `json:"createdAt"`
	Author    ghGraphQLActor `json:"author"`
}

// ghActivityThread is one inline review thread. Named rather than inlined so
// tests can build one: the reply-in-a-thread case is exactly what the old
// fingerprint could not see, so it needs to be easy to write a test for.
type ghActivityThread struct {
	Comments struct {
		Nodes []ghActivityNode `json:"nodes"`
	} `json:"comments"`
}

type ghActivityResp struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				Comments struct {
					Nodes []ghActivityNode `json:"nodes"`
				} `json:"comments"`
				Reviews struct {
					Nodes []ghActivityNode `json:"nodes"`
				} `json:"reviews"`
				ReviewThreads struct {
					Nodes []ghActivityThread `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

// LastHumanActivity returns the most recent time a person other than selfLogin
// said something on the PR. Zero time means nobody has. Bots are excluded by
// typename, and selfLogin is excluded by login so our own posted review never
// reads as somebody responding to it.
func LastHumanActivity(ctx context.Context, repo string, number int, selfLogin string) (time.Time, error) {
	resp, err := ghGraphQL[ghActivityResp](ctx, repo, fmt.Sprintf("human activity for %s#%d", repo, number),
		humanActivityQuery, "-F", fmt.Sprintf("number=%d", number))
	if err != nil {
		return time.Time{}, err
	}
	return latestHumanActivity(resp, selfLogin), nil
}

// latestHumanActivity is the pure reduction over a fetched activity response,
// extracted so the two exclusions that matter can be table-tested without gh:
// bots (they comment constantly and none of it is a reason to review again) and
// ourselves (our own review is not somebody responding to our review).
func latestHumanActivity(resp ghActivityResp, selfLogin string) time.Time {
	pr := resp.Data.Repository.PullRequest
	var latest time.Time
	consider := func(nodes []ghActivityNode) {
		for _, n := range nodes {
			if n.Author.Typename != "User" || sameLogin(n.Author.Login, selfLogin) {
				continue
			}
			if n.CreatedAt.After(latest) {
				latest = n.CreatedAt
			}
		}
	}
	consider(pr.Comments.Nodes)
	consider(pr.Reviews.Nodes)
	for _, t := range pr.ReviewThreads.Nodes {
		consider(t.Comments.Nodes)
	}
	return latest
}
