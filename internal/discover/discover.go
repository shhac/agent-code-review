// Package discover finds candidate PRs deterministically via the gh CLI and
// reconciles them into the store. It replaces the "set up a python script"
// step from the original schedule with native Go: `gh pr list --json` per repo,
// then the New/Refreshed rules applied in-process. Refreshed detection joins
// against the store's review history (last reviewed head SHA).
package discover

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/shhac/crew-code-review/internal/config"
	"github.com/shhac/crew-code-review/internal/store"
)

// Clock is injectable so tests don't depend on wall time.
type Clock func() time.Time

// Logf is a minimal logging sink (fmt.Printf-shaped).
type Logf func(format string, args ...any)

// candidateStore is the narrow slice of the store discovery actually uses:
// enqueueing classified candidates, reading history for Refreshed detection
// (last real review) and same-SHA suppression (last outcome of any verdict),
// and the author's group membership, which decides whether we look at their
// PRs at all. Consumer-defined so tests fake four methods, not twenty.
type candidateStore interface {
	Enqueue(ctx context.Context, c store.Candidate) error
	LastReview(ctx context.Context, repo string, number int) (store.Review, bool, error)
	LastOutcome(ctx context.Context, repo string, number int) (store.Review, bool, error)
	AuthorGroup(ctx context.Context, repo, handle string) (config.Membership, error)
}

// Discoverer turns config + gh + store into fresh queue entries. Config is a
// getter so watched repos, author scoping, and age windows apply live.
type Discoverer struct {
	cfg   func() config.Config
	store candidateStore
	now   Clock
	logf  Logf
	// warnf is logf's severity-carrying sibling, used for the failure and
	// backoff lines. Discovery skipping a repo is the one thing in this
	// package somebody grepping a log actually needs to find.
	warnf Logf
	// mu guards backoff and resume: sweeps are serialised by the scheduler,
	// but the Discoverer outlives any one of them.
	mu      sync.Mutex
	backoff map[string]repoBackoff
	resume  string
	// listPRs fetches one repo's open PRs (gh in production; injected in
	// tests so the sweep's per-repo resilience is testable without gh).
	listPRs func(ctx context.Context, repo string) ([]ghPR, error)
	// lastHumanActivity returns the newest time somebody other than us said
	// something on a PR (gh in production; injected in tests, same reason).
	lastHumanActivity func(ctx context.Context, repo string, number int) (time.Time, error)
	// selfLogin is our own gh handle, excluded from human activity so our own
	// posted review never reads as somebody responding to it.
	selfLogin string
}

func New(cfg func() config.Config, s candidateStore, logf Logf) *Discoverer {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	d := &Discoverer{cfg: cfg, store: s, now: time.Now, logf: logf, warnf: logf, backoff: map[string]repoBackoff{}}
	d.listPRs = d.ghListPRs
	d.lastHumanActivity = func(ctx context.Context, repo string, number int) (time.Time, error) {
		return LastHumanActivity(ctx, repo, number, d.selfLogin)
	}
	return d
}

// WithWarnf routes the failure and backoff lines to a severity-carrying sink.
// Without it they fall back to logf, so a caller that has only one sink keeps
// the previous behaviour.
func (d *Discoverer) WithWarnf(warnf Logf) *Discoverer {
	if warnf != nil {
		d.warnf = warnf
	}
	return d
}

// WithSelfLogin records our own gh handle so Discussion detection can tell our
// own review apart from somebody replying to it. Without it every review we
// post would look like new conversation about itself.
func (d *Discoverer) WithSelfLogin(login string) *Discoverer {
	d.selfLogin = login
	return d
}

// Discover lists PRs across all configured repos, classifies each as New or
// Refreshed (or neither), upserts the matches into the store, and returns them.
// A repo that fails to list (bad name, auth hiccup) is logged and skipped so it
// can't take down the whole cycle; an error is returned only when every repo
// failed, since that usually means gh itself is broken.
func (d *Discoverer) Discover(ctx context.Context) ([]store.Candidate, error) {
	var found []store.Candidate
	var lastErr error
	failed, backedOff := 0, 0
	cfg := d.cfg()

	// One sweep may not outlast the gap before the next one. The repo list is
	// walked from wherever the last sweep ran out of budget, so a repo that
	// pages slowly delays its neighbours by a cycle instead of starving them
	// forever. See resumeAt.
	budget := cfg.DiscoverySweepBudget()
	deadline := d.now().Add(budget)
	repos := d.rotate(cfg.Repos)

	for i, repo := range repos {
		if now := d.now(); now.After(deadline) {
			d.resumeAt(cfg.Repos, repo)
			d.warnf("discover: sweep budget %s spent after %d of %d repo(s), resuming at %s next cycle",
				budget, i, len(repos), repo)
			break
		}
		if until, fails, held := d.inBackoff(repo, d.now()); held {
			d.warnf("discover %s: in backoff after %d consecutive failure(s), skipping until %s (%s left)",
				repo, fails, until.UTC().Format(time.RFC3339), until.Sub(d.now()).Round(time.Second))
			backedOff++
			continue
		}
		prs, err := d.listPRs(ctx, repo)
		if err != nil {
			fails, wait := d.noteFailure(repo, d.now())
			d.warnf("discover %s: %v, skipping repo this cycle (consecutive failure %d, backing off %s)",
				repo, err, fails, wait)
			failed++
			lastErr = err
			continue
		}
		if fails := d.noteSuccess(repo); fails > 0 {
			d.logf("discover %s: recovered after %d consecutive failure(s)", repo, fails)
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
	// An error means "gh itself is broken", so it needs every repo to be
	// unusable AND at least one of them to have proved it this cycle. Repos
	// skipped because they are already in backoff do not re-prove anything:
	// counting them would turn one outage into an error on every subsequent
	// cycle, which is the cascade backoff exists to prevent.
	if failed > 0 && failed+backedOff == len(cfg.Repos) {
		return nil, fmt.Errorf("discovery failed for all %d repos: %w", len(cfg.Repos), lastErr)
	}
	return found, nil
}

// rotate returns the watch list starting at the repo a budget-truncated sweep
// stopped on, so the tail of the list is not permanently unreachable. A repo
// that has since left the config drops the resume point rather than skipping
// the sweep.
func (d *Discoverer) rotate(repos []string) []string {
	d.mu.Lock()
	resume := d.resume
	d.mu.Unlock()
	if resume == "" {
		return repos
	}
	for i, r := range repos {
		if r == resume {
			return append(append([]string{}, repos[i:]...), repos[:i]...)
		}
	}
	return repos
}

// resumeAt remembers where the next sweep should start. Clearing it when repo
// is the head of the list keeps a sweep that always runs out of budget on its
// first repo from pinning the rotation there.
func (d *Discoverer) resumeAt(repos []string, repo string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(repos) > 0 && repos[0] == repo {
		d.resume = ""
		return
	}
	d.resume = repo
}
