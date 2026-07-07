// Package scheduler owns the deterministic review cycle: take the run-lock,
// discover candidates, process the queue oldest-first up to the parallelism
// cap, record verdicts, release the lock. The serve daemon calls RunCycle on a
// ticker; `run --once` calls it a single time.
package scheduler

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/discover"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
)

// Logf is a minimal logging sink (fmt.Printf-shaped).
type Logf func(format string, args ...any)

// Scheduler wires the deterministic machinery around a review engine.
type Scheduler struct {
	cfg    config.Config
	store  store.Store
	disc   *discover.Discoverer
	engine review.Engine
	ghUser string
	logf   Logf
}

func New(cfg config.Config, s store.Store, d *discover.Discoverer, e review.Engine, ghUser string, logf Logf) *Scheduler {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Scheduler{cfg: cfg, store: s, disc: d, engine: e, ghUser: ghUser, logf: logf}
}

// Start runs RunCycle immediately, then on the configured interval until ctx
// is cancelled.
func (s *Scheduler) Start(ctx context.Context) error {
	interval := s.cfg.Interval()
	s.logf("scheduler: starting, interval %s, max parallel %d", interval, s.cfg.MaxParallel())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := s.RunCycle(ctx); err != nil {
			s.logf("cycle error: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// RunCycle performs one review cycle. It is a no-op (returns nil) when another
// cycle is still in flight — the run-lock rule from the schedule spec.
func (s *Scheduler) RunCycle(ctx context.Context) error {
	s.logf("cycle: started at %s", time.Now().Format(time.RFC3339))

	staleAfter := s.cfg.Interval() * 4
	if _, active, err := s.store.ActiveRun(ctx, staleAfter); err != nil {
		return err
	} else if active {
		s.logf("cycle: a previous run is still active — skipping")
		return nil
	}

	run := store.Run{ID: newRunID(), StartedAt: time.Now(), Host: hostname(), PID: os.Getpid()}
	if err := s.store.StartRun(ctx, run); err != nil {
		return err
	}
	status := "done"
	defer func() {
		if err := s.store.FinishRun(ctx, run.ID, status); err != nil {
			s.logf("cycle: finish run: %v", err)
		}
		s.logf("cycle: finished at %s (%s)", time.Now().Format(time.RFC3339), status)
	}()

	if _, err := s.disc.Discover(ctx); err != nil {
		status = "failed"
		return err
	}

	queued, err := s.store.ListCandidates(ctx, store.Filter{Status: store.StatusQueued})
	if err != nil {
		status = "failed"
		return err
	}
	if len(queued) == 0 {
		s.logf("cycle: no candidates")
		return nil
	}
	s.logf("cycle: %d candidate(s) to review", len(queued))
	s.processQueue(ctx, queued)
	return nil
}

// processQueue reviews candidates concurrently, capped at MaxParallel. The
// input is already sorted New-before-Refreshed, oldest-first by the store.
func (s *Scheduler) processQueue(ctx context.Context, candidates []store.Candidate) {
	sem := make(chan struct{}, s.cfg.MaxParallel())
	var wg sync.WaitGroup
	for _, c := range candidates {
		wg.Add(1)
		sem <- struct{}{}
		go func(c store.Candidate) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := s.reviewOne(ctx, c); err != nil {
				s.logf("review %s#%d: %v", c.Repo, c.Number, err)
			}
		}(c)
	}
	wg.Wait()
}

// reviewOne runs the engine against a single candidate and records the verdict.
func (s *Scheduler) reviewOne(ctx context.Context, c store.Candidate) error {
	if err := s.store.SetStatus(ctx, c.Repo, c.Number, store.StatusReviewing); err != nil {
		return err
	}

	workDir, err := os.MkdirTemp("", fmt.Sprintf("agent-code-review-%d-", c.Number))
	if err != nil {
		return err
	}
	// Leave the tmp dir in place — a future run may reuse it (per the spec).

	allowed, err := s.store.IsAuthorAllowed(ctx, c.Repo, c.Author)
	if err != nil {
		return err
	}
	facts := review.DeriveFacts(c, s.ghUser, allowed)
	prompt := review.BuildPrompt(s.cfg, c, facts)

	verdict, reviewErr := s.engine.Review(ctx, review.Request{Candidate: c, Prompt: prompt, WorkDir: workDir})
	if verdict.Summary != "" {
		s.logf("review %s#%d: %s — %s", c.Repo, c.Number, verdict.Decision, verdict.Summary)
	}

	// Record history only when the agent actually reviewed (approved or
	// commented). A skip or failure must NOT count as "reviewed at this SHA",
	// or Refreshed detection would never re-surface the PR.
	if verdict.Decision == review.DecisionApproved || verdict.Decision == review.DecisionCommented {
		if err := s.store.RecordReview(ctx, store.Review{
			Repo:       c.Repo,
			Number:     c.Number,
			HeadSHA:    c.HeadSHA,
			Verdict:    verdict.Decision,
			Engine:     s.engine.Name(),
			ReviewedAt: time.Now(),
		}); err != nil {
			return err
		}
	}

	status := statusFor(verdict.Decision)
	if err := s.store.SetStatus(ctx, c.Repo, c.Number, status); err != nil {
		return err
	}
	return reviewErr
}

// statusFor maps the agent's reported decision onto a queue status.
func statusFor(decision string) string {
	switch decision {
	case review.DecisionApproved, review.DecisionCommented:
		return store.StatusReviewed
	case review.DecisionSkipped:
		return store.StatusSkipped
	default:
		return store.StatusError
	}
}

func newRunID() string { return fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid()) }

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}
