// Package discover finds candidate PRs deterministically via the gh CLI and
// reconciles them into the store. It replaces the "set up a python script"
// step from the original schedule with native Go: `gh pr list --json` per repo,
// then the New/Refreshed rules applied in-process. Refreshed detection joins
// against the store's review history (last reviewed head SHA).
package discover

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
)

// Clock is injectable so tests don't depend on wall time.
type Clock func() time.Time

// Logf is a minimal logging sink (fmt.Printf-shaped).
type Logf func(format string, args ...any)

// candidateStore is the narrow slice of the store discovery actually uses:
// enqueueing classified candidates, reading history for Refreshed detection
// (last real review) and same-SHA suppression (last outcome of any verdict),
// and the allowed-authors check for author-scoped repos. Consumer-defined so
// tests fake four methods, not twenty.
type candidateStore interface {
	Enqueue(ctx context.Context, c store.Candidate) error
	LastReview(ctx context.Context, repo string, number int) (store.Review, bool, error)
	LastOutcome(ctx context.Context, repo string, number int) (store.Review, bool, error)
	IsAuthorAllowed(ctx context.Context, repo, handle string) (bool, error)
}

// Discoverer turns config + gh + store into fresh queue entries. Config is a
// getter so watched repos, author scoping, and age windows apply live.
type Discoverer struct {
	cfg   func() config.Config
	store candidateStore
	now   Clock
	logf  Logf
}

func New(cfg func() config.Config, s candidateStore, logf Logf) *Discoverer {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Discoverer{cfg: cfg, store: s, now: time.Now, logf: logf}
}

// Discover lists PRs across all configured repos, classifies each as New or
// Refreshed (or neither), upserts the matches into the store, and returns them.
// A repo that fails to list (bad name, auth hiccup) is logged and skipped so it
// can't take down the whole cycle; an error is returned only when every repo
// failed, since that usually means gh itself is broken.
func (d *Discoverer) Discover(ctx context.Context) ([]store.Candidate, error) {
	var found []store.Candidate
	var lastErr error
	failed := 0
	cfg := d.cfg()
	for _, repo := range cfg.Repos {
		prs, err := d.listPRs(ctx, repo)
		if err != nil {
			d.logf("discover %s: %v — skipping repo this cycle", repo, err)
			failed++
			lastErr = err
			continue
		}
		for _, pr := range prs {
			cand, ok, err := d.classify(ctx, cfg, repo, pr)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			if err := d.store.Enqueue(ctx, cand); err != nil {
				return nil, err
			}
			found = append(found, cand)
		}
	}
	if failed > 0 && failed == len(cfg.Repos) {
		return nil, fmt.Errorf("discovery failed for all %d repos: %w", failed, lastErr)
	}
	return found, nil
}

// listPRs fetches open PRs for one repo with the fields we classify on.
func (d *Discoverer) listPRs(ctx context.Context, repo string) ([]ghPR, error) {
	out, err := runGH(ctx, "pr", "list",
		"--repo", repo,
		"--state", "open",
		"--limit", "100",
		"--json", prListFields,
	)
	if err != nil {
		return nil, err
	}
	var prs []ghPR
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, err
	}
	return prs, nil
}

// candidacyGate is the shared "is this PR reviewable work?" predicate: not a
// draft, an outstanding review request, not currently approved. classify and
// the scheduler's pre-review recheck (StillCandidate) both use it, so the two
// decisions cannot drift. The returned reason names the failed gate.
func candidacyGate(pr ghPR) (bool, string) {
	if pr.IsDraft {
		return false, "draft"
	}
	if !pr.hasOpenReviewRequest() {
		return false, "no open review request"
	}
	// An approved PR is already unblocked — nothing for this tool to do.
	if pr.isApproved() {
		return false, "already approved"
	}
	return true, ""
}

// classify applies the New then Refreshed rules. New wins if both could match.
// cfg is the sweep's snapshot, threaded from Discover so every PR in one
// sweep is judged against one coherent config.
func (d *Discoverer) classify(ctx context.Context, cfg config.Config, repo string, pr ghPR) (store.Candidate, bool, error) {
	if ok, _ := candidacyGate(pr); !ok {
		return store.Candidate{}, false, nil
	}
	// Author-scoped repos only discover PRs from allowed authors; everywhere
	// else any open PR is fair game (the allow-list then only governs whether
	// an APPROVE is permitted).
	if cfg.AuthorScopedRepo(repo) {
		allowed, err := d.store.IsAuthorAllowed(ctx, repo, pr.Author.Login)
		if err != nil {
			return store.Candidate{}, false, err
		}
		if !allowed {
			return store.Candidate{}, false, nil
		}
	}
	now := d.now()

	// Suppression: any recorded outcome — real review, skip, or error — at
	// the PR's CURRENT head SHA means there is nothing new to do; without
	// this every sweep would re-enqueue skipped PRs (and re-enqueue reviewed
	// ones whenever the engine's posted review hasn't landed on gh yet).
	// New commits change the SHA and re-enqueue naturally.
	if outcome, ok, err := d.store.LastOutcome(ctx, repo, pr.Number); err != nil {
		return store.Candidate{}, false, err
	} else if ok && outcome.HeadSHA == pr.HeadRefOID {
		return store.Candidate{}, false, nil
	}

	// NEW: never reviewed by anyone, within the New window.
	if !pr.hasAnyReview() && now.Sub(pr.CreatedAt) <= cfg.NewMaxAge() {
		return d.toCandidate(repo, pr, store.TypeNew, now), true, nil
	}

	// REFRESHED: we reviewed it before, at a different head SHA, within the
	// Refreshed window. "Reviewed by us" means a real verdict in our own
	// history (LastReview filters out SKIPPED/ERROR), not gh state. The SHA
	// inequality is redundant while every real review also lands in history
	// (suppression above already returned for a current-SHA outcome) — kept
	// as cheap insurance so Refreshed stays correct even if that invariant
	// ever breaks.
	last, ok, err := d.store.LastReview(ctx, repo, pr.Number)
	if err != nil {
		return store.Candidate{}, false, err
	}
	if ok && last.HeadSHA != pr.HeadRefOID && now.Sub(pr.CreatedAt) <= cfg.RefreshedMaxAge() {
		return d.toCandidate(repo, pr, store.TypeRefreshed, now), true, nil
	}

	return store.Candidate{}, false, nil
}

func (d *Discoverer) toCandidate(repo string, pr ghPR, typ string, now time.Time) store.Candidate {
	return store.Candidate{
		Repo:         repo,
		Number:       pr.Number,
		Type:         typ,
		Title:        pr.Title,
		Author:       pr.Author.Login,
		URL:          pr.URL,
		HeadSHA:      pr.HeadRefOID,
		CreatedAt:    pr.CreatedAt,
		UpdatedAt:    pr.UpdatedAt,
		DiscoveredAt: now,
		Source:       store.SourceDiscovered,
	}
}
