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
// upserting classified candidates and reading our last review for Refreshed
// detection. Consumer-defined so tests fake two methods, not twenty.
type candidateStore interface {
	UpsertCandidate(ctx context.Context, c store.Candidate) error
	LastReview(ctx context.Context, repo string, number int) (store.Review, bool, error)
}

// Discoverer turns config + gh + store into fresh queue entries.
type Discoverer struct {
	cfg   config.Config
	store candidateStore
	now   Clock
	logf  Logf
}

func New(cfg config.Config, s candidateStore, logf Logf) *Discoverer {
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
	for _, repo := range d.cfg.Repos {
		prs, err := d.listPRs(ctx, repo)
		if err != nil {
			d.logf("discover %s: %v — skipping repo this cycle", repo, err)
			failed++
			lastErr = err
			continue
		}
		for _, pr := range prs {
			cand, ok, err := d.classify(ctx, repo, pr)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			if err := d.store.UpsertCandidate(ctx, cand); err != nil {
				return nil, err
			}
			found = append(found, cand)
		}
	}
	if failed > 0 && failed == len(d.cfg.Repos) {
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

// classify applies the New then Refreshed rules. New wins if both could match.
func (d *Discoverer) classify(ctx context.Context, repo string, pr ghPR) (store.Candidate, bool, error) {
	if pr.IsDraft || !pr.hasOpenReviewRequest() {
		return store.Candidate{}, false, nil
	}
	now := d.now()

	// NEW: never reviewed by anyone, within the New window.
	if !pr.hasAnyReview() && now.Sub(pr.CreatedAt) <= d.cfg.NewMaxAge() {
		return d.toCandidate(repo, pr, store.TypeNew, now), true, nil
	}

	// REFRESHED: we reviewed it before, at a different head SHA, within the
	// Refreshed window. "Reviewed by us" comes from our own store, not gh.
	last, ok, err := d.store.LastReview(ctx, repo, pr.Number)
	if err != nil {
		return store.Candidate{}, false, err
	}
	if ok && last.HeadSHA != pr.HeadRefOID && now.Sub(pr.CreatedAt) <= d.cfg.RefreshedMaxAge() {
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
		Status:       store.StatusQueued,
		DiscoveredAt: now,
	}
}
