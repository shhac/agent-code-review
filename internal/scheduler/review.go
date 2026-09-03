package scheduler

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
)

// pending is a queued candidate paired with its author's resolved policy and
// the config snapshot it was resolved under. All three travel together from
// the moment the dispatcher pulls the candidate, because every decision left
// (which engine, whether its headroom allows a start, what the prompt says)
// must read the same answer. The config rides along rather than being
// re-read by the worker: a candidate that cleared the usage floor under one
// config must not then be built and prompted under another.
type pending struct {
	candidate store.Candidate
	policy    config.Policy
	cfg       config.Config
}

// runOne is the worker body: build this candidate's engine, then review it.
// The engine is built per candidate rather than per batch because an author's
// group can name its own engine, model and effort, so two concurrent reviews
// can run different CLIs.
//
// A non-nil error means the attempt produced no recorded outcome, which is
// what the dispatcher's backoff keys on: both failure paths below leave the
// queue row exactly as they found it, and the dispatcher always offers the
// head first.
func (s *Scheduler) runOne(ctx context.Context, p pending) error {
	// Built before the claim, so a group pointing at an unbuildable engine
	// leaves its candidate pending and retryable rather than claimed and
	// stuck. Boot validation covers the reachable set, so reaching here means
	// config changed under a running daemon.
	engine, err := s.newEngine(p.cfg, p.policy)
	if err != nil {
		err = fmt.Errorf("build %s engine: %w", p.cfg.EngineFor(p.policy), err)
		s.logf("review %s#%d: %v", p.candidate.Repo, p.candidate.Number, err)
		return err
	}
	if err := s.reviewOne(ctx, p, p.cfg, engine); err != nil {
		s.logf("review %s#%d: %v", p.candidate.Repo, p.candidate.Number, err)
		return err
	}
	return nil
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
	// The head is passed so the recheck can also answer "have we already
	// reviewed THIS revision": an attempt interrupted after it posted recorded
	// nothing, so without this the work would be done twice.
	ok, reason, err := s.stillCandidate(ctx, c.Repo, c.Number, s.ghUser, c.HeadSHA)
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
func (s *Scheduler) reviewOne(ctx context.Context, p pending, cfg config.Config, engine review.Engine) error {
	c := p.candidate
	// A work_dir already on the row is the previous claim's: this candidate is
	// back in the queue because a daemon died mid-review. Read before the
	// claim overwrites it, because its log is the only surviving record of the
	// session that attempt had open.
	resumeSession := ""
	if c.WorkDir != "" {
		resumeSession = review.SessionFromLog(c.WorkDir)
	}

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
		// The directory only earns its keep once a claim records it: nothing
		// points at this one, so nothing would ever read or remove it. The
		// lost-the-claim path below cleans up for the same reason; this one
		// used to return straight past it and leak a directory per failure.
		_ = os.Remove(workDir)
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
	skipped, err := s.skipIfStale(ctx, c, claimedAt)
	if err != nil {
		return err
	}
	if skipped {
		return nil
	}
	// Leave the tmp dir in place; a future run may reuse it (per the spec).

	facts := review.DeriveFacts(c, s.ghUser, p.policy)
	prompt := review.BuildPrompt(cfg, c, facts)

	if resumeSession != "" {
		s.logf("review %s#%d: resuming session %s from an interrupted attempt", c.Repo, c.Number, resumeSession)
	}
	verdict, reviewErr := engine.Review(ctx, review.Request{
		Candidate: c, Prompt: prompt, WorkDir: workDir, ResumeSession: resumeSession,
	})
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
	if err := s.store.Complete(ctx, reviewRecord(c, verdict, engine.Provenance(ctx), claimedAt, s.priceFn)); err != nil {
		return err
	}
	return reviewErr
}

// reviewRecord builds the history row for one engine outcome: ReviewFrom's
// candidate snapshot plus the engine-reported provenance and spend. The
// companion to store.ReviewFrom, so a new provenance field has exactly one
// place to be threaded.
func reviewRecord(c store.Candidate, v review.Verdict, p review.Provenance, claimedAt time.Time, price PriceFn) store.Review {
	rec := store.ReviewFrom(c, v.Decision, p.Engine, claimedAt)
	rec.Model = p.Model
	rec.Effort = p.Effort
	rec.EngineVersion = p.EngineVersion
	rec.TokensUsed = v.Tokens.Total()
	rec.FreshTokens = v.Tokens.Fresh()
	rec.InputTokens = v.Tokens.Input
	rec.OutputTokens = v.Tokens.Output
	rec.CacheWriteTokens = v.Tokens.CacheWrite
	rec.CacheReadTokens = v.Tokens.CacheRead
	rec.ReasoningTokens = v.Tokens.Reasoning
	rec.UsageRaw = v.UsageRaw
	rec.CostUSD = v.CostUSD
	// Our own valuation, frozen here at the rates in force now. Recorded even
	// when the engine reported its own: the two side by side are the only
	// check that our class mapping and rates are right.
	if price != nil {
		if est, ok := price(rec.Model, rec.InputTokens, rec.OutputTokens, rec.CacheWriteTokens, rec.CacheReadTokens); ok {
			rec.EstCostUSD = est
		}
	}
	return rec
}

// tail returns the last n bytes of s, whitespace-trimmed, newlines flattened.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = "…" + s[len(s)-n:]
	}
	return strings.ReplaceAll(s, "\n", " | ")
}
