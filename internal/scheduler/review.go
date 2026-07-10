package scheduler

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
)

// processQueue reviews candidates concurrently, capped at cfg.MaxParallel.
// The input is already sorted New-before-Refreshed, oldest-first by the
// store. The cycle's config snapshot and engine travel as parameters so
// every goroutine works from one coherent config; nothing cycle-scoped
// lives on the long-lived Scheduler struct.
func (s *Scheduler) processQueue(stopCtx, reviewCtx context.Context, candidates []store.Candidate, cfg config.Config, engine review.Engine) {
	sem := make(chan struct{}, cfg.MaxParallel())
	var wg sync.WaitGroup
	for _, c := range candidates {
		select {
		case sem <- struct{}{}:
		case <-stopCtx.Done():
			s.logf("cycle: shutdown requested, waiting for in-flight reviewer(s)")
			wg.Wait()
			return
		case <-reviewCtx.Done():
			wg.Wait()
			return
		}
		wg.Add(1)
		go func(c store.Candidate) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := s.reviewOne(reviewCtx, c, cfg, engine); err != nil {
				s.logf("review %s#%d: %v", c.Repo, c.Number, err)
			}
		}(c)
	}
	wg.Wait()
}

// skipIfStale re-validates a discovered candidate just before the engine
// spend: PRs approved, merged, or closed while waiting in the queue complete
// as a precheck SKIPPED instead of being reviewed. Manual adds bypass the
// check; explicit re-review requests and draft reviews must always go
// through. A recheck error propagates with nothing recorded; the claim stays,
// and the stale lease retries next cycle.
func (s *Scheduler) skipIfStale(ctx context.Context, c store.Candidate, started time.Time) (bool, error) {
	if c.Source == store.SourceManual {
		return false, nil
	}
	ok, reason, err := s.stillCandidate(ctx, c.Repo, c.Number)
	if err != nil {
		return false, fmt.Errorf("candidacy recheck: %w", err)
	}
	if ok {
		return false, nil
	}
	s.logf("review %s#%d: no longer a candidate (%s), recording skip", c.Repo, c.Number, reason)
	return true, s.store.Complete(ctx, store.ReviewFrom(c, review.DecisionSkipped, store.EnginePrecheck, started))
}

// reviewOne claims a candidate, rechecks its candidacy, runs the engine, and
// completes it: every outcome (including SKIPPED/ERROR) is recorded in
// history as the queue row is removed (atomically, SHA-gated; see
// Store.Complete).
func (s *Scheduler) reviewOne(ctx context.Context, c store.Candidate, cfg config.Config, engine review.Engine) error {
	// The workdir exists before the claim so the claim can record it: from
	// that moment <work_dir>/agent.log is the candidate's live review log.
	workDir, err := os.MkdirTemp("", fmt.Sprintf("agent-code-review-%d-", c.Number))
	if err != nil {
		return err
	}
	c.WorkDir = workDir
	claimedAt := time.Now()
	claimed, err := s.store.Claim(ctx, c.Repo, c.Number, store.Lease{
		At: claimedAt, WorkDir: workDir, Host: hostname(), PID: os.Getpid(), StaleAfter: cfg.LeaseWindow(),
	})
	if err != nil {
		return err
	}
	// Lost the compare-and-swap: another worker (possibly another daemon
	// instance sharing the store) claimed it between our queue listing and
	// now. Their review proceeds; nothing to record here.
	if !claimed {
		s.logf("review %s#%d: claimed by another worker, skipping", c.Repo, c.Number)
		_ = os.Remove(workDir)
		return nil
	}
	if skipped, err := s.skipIfStale(ctx, c, claimedAt); skipped || err != nil {
		return err
	}
	// Leave the tmp dir in place; a future run may reuse it (per the spec).

	allowed, err := s.store.IsAuthorAllowed(ctx, c.Repo, c.Author)
	if err != nil {
		return err
	}
	facts := review.DeriveFacts(c, s.ghUser, allowed)
	prompt := review.BuildPrompt(cfg, c, facts)

	verdict, reviewErr := engine.Review(ctx, review.Request{Candidate: c, Prompt: prompt, WorkDir: workDir})
	if verdict.Summary != "" {
		s.logf("review %s#%d: %s: %s", c.Repo, c.Number, verdict.Decision, verdict.Summary)
	}
	// A failed invocation's only clue is the engine's own output; surface its
	// tail instead of a bare exit status.
	if reviewErr != nil && verdict.Raw != "" {
		s.logf("review %s#%d: engine output tail: %s", c.Repo, c.Number, tail(verdict.Raw, 500))
	}

	// Every outcome goes to history, SKIPPED/ERROR included. They don't
	// block a future re-review: store.LastReview filters them out of
	// Refreshed detection, and new commits change the SHA that discovery's
	// same-SHA suppression keys on.
	provenance := engine.Provenance(ctx)
	rec := store.ReviewFrom(c, verdict.Decision, provenance.Engine, claimedAt)
	rec.Model = provenance.Model
	rec.Effort = provenance.Effort
	rec.CodexVersion = provenance.CodexVersion
	rec.TokensUsed = verdict.TokensUsed
	if err := s.store.Complete(ctx, rec); err != nil {
		return err
	}
	return reviewErr
}

// tail returns the last n bytes of s, whitespace-trimmed, newlines flattened.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = "…" + s[len(s)-n:]
	}
	return strings.ReplaceAll(s, "\n", " | ")
}
