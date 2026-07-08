// Package scheduler owns the deterministic review cycle: take the run-lock,
// discover candidates, process the queue oldest-first up to the parallelism
// cap, record verdicts, release the lock. The serve daemon runs Discover and
// ReviewCycle as independent heartbeat loops (Start); `run --once` calls
// RunCycle a single time.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/discover"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
	"github.com/shhac/agent-code-review/internal/usage"
)

// Logf is a minimal logging sink (fmt.Printf-shaped).
type Logf func(format string, args ...any)

// UsageFn supplies the latest Codex usage snapshot. Callers with no usage
// data (one-shot runs) pass nil; New normalizes that to an empty-snapshot
// getter, so the fail-open rule lives in exactly one place —
// usage.BelowFloor, which never pauses on an empty snapshot.
type UsageFn func() usage.Snapshot

// Scheduler wires the deterministic machinery around a review engine. Config
// comes through a getter so edits to config.json (cadence, parallelism,
// usage floors, codex settings) apply without a restart; only the loop
// on/off switches are fixed at boot, because the --no-* flags own them.
type Scheduler struct {
	cfg         func() config.Config
	store       store.Store
	disc        *discover.Discoverer
	ghUser      string
	logf        Logf
	usageFn     UsageFn
	discovering atomic.Bool // in-flight guard for the discovery sweep

	// newEngine builds the review engine from live config at the start of
	// each cycle, so codex.* edits apply without a restart.
	newEngine func(config.Config) (review.Engine, error)
	// stillCandidate re-checks a PR's candidacy just before the engine runs
	// (discover.StillCandidate in production; swapped in tests).
	stillCandidate func(ctx context.Context, repo string, number int) (bool, string, error)
	// pidAlive reports whether a pid is a live process on THIS host (real
	// signal-0 probe in production; swapped in tests). Reconcile uses it to
	// tell a crashed daemon's leftovers from a sibling instance's live work.
	pidAlive func(pid int) bool
}

func New(cfg func() config.Config, s store.Store, d *discover.Discoverer, ghUser string, logf Logf, usageFn UsageFn) *Scheduler {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if usageFn == nil {
		usageFn = func() usage.Snapshot { return usage.Snapshot{} }
	}
	return &Scheduler{
		cfg: cfg, store: s, disc: d, ghUser: ghUser,
		logf: logf, usageFn: usageFn,
		newEngine:      func(c config.Config) (review.Engine, error) { return review.NewEngine(c.Review) },
		stillCandidate: discover.StillCandidate,
		pidAlive:       pidAlive,
	}
}

// Start runs the enabled loops until ctx is cancelled. Cancellation is forceful:
// in-flight reviewers receive ctx too. The serve daemon uses StartGraceful so
// its first Ctrl-C can stop scheduling while letting claimed reviewers finish.
func (s *Scheduler) Start(ctx context.Context) error {
	return s.StartGraceful(ctx, ctx)
}

// StartGraceful runs the enabled loops until stopCtx is cancelled: discovery
// receives stopCtx and is cancelled immediately, while in-flight reviewers
// receive reviewCtx and drain unless that second context is cancelled too.
// Enabled loops fire immediately on start.
func (s *Scheduler) StartGraceful(stopCtx, reviewCtx context.Context) error {
	// A crashed daemon leaves a running run row (which would block cycles
	// for the whole lease window) and claimed queue rows (which would wait
	// it out too). Reconcile before the first tick so a restart resumes
	// immediately. Failure is logged, not fatal — the lease window is the
	// fallback that always works.
	if err := s.Reconcile(reviewCtx); err != nil {
		s.logf("reconcile: %v", err)
	}
	boot := s.cfg()
	var wg sync.WaitGroup
	started := false
	if boot.Discovery.Enabled {
		s.logf("scheduler: discovery every %s (config reloads live)", boot.DiscoverInterval())
		started = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.loop(stopCtx, func() time.Duration { return s.cfg().DiscoverInterval() }, "discover", s.Discover)
		}()
	}
	if boot.Schedule.Enabled {
		s.logf("scheduler: reviews every %s, max parallel %d (config reloads live)", boot.Interval(), boot.MaxParallel())
		started = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.loop(stopCtx, func() time.Duration { return s.cfg().Interval() }, "review", func(context.Context) error {
				return s.reviewCycle(stopCtx, reviewCtx)
			})
		}()
	}
	if !started {
		<-stopCtx.Done()
		return stopCtx.Err()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return stopCtx.Err()
	case <-reviewCtx.Done():
		return reviewCtx.Err()
	}
}

// loopHeartbeat is how often a loop re-reads its interval, so a cadence edit
// in config.json takes effect within this bound instead of after the
// previously scheduled tick.
const loopHeartbeat = 30 * time.Second

// due reports whether interval has elapsed since the last run started. The
// heartbeat evaluates it against the LIVE interval, so shrinking the cadence
// in config.json can make an already-elapsed run due on the next beat.
func due(last, now time.Time, interval time.Duration) bool {
	return now.Sub(last) >= interval
}

// loop runs fn immediately, then whenever interval() has elapsed since the
// last run started.
func (s *Scheduler) loop(ctx context.Context, interval func() time.Duration, name string, fn func(context.Context) error) {
	run := func() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := fn(ctx); err != nil {
			s.logf("%s error: %v", name, err)
		}
	}
	last := time.Now()
	run()
	ticker := time.NewTicker(loopHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if due(last, time.Now(), interval()) {
				last = time.Now()
				run()
			}
		}
	}
}

// Discover scrapes the watched repos for candidates. Purely deterministic —
// gh + classification rules, no LLM involved. A guard skips the sweep when
// the previous one is still in flight.
func (s *Scheduler) Discover(ctx context.Context) error {
	if !s.discovering.CompareAndSwap(false, true) {
		s.logf("discover: previous sweep still running — skipping")
		return nil
	}
	defer s.discovering.Store(false)
	found, err := s.disc.Discover(ctx)
	if err != nil {
		return err
	}
	if len(found) > 0 {
		s.logf("discover: %d candidate(s) upserted", len(found))
	}
	return nil
}

// ReviewCycle processes the queued candidates. It is a no-op (returns nil)
// when another cycle is still in flight — the run-lock rule from the spec.
// An idle cycle (nothing available to review) exits before the run-lock and
// records nothing: with the default 1m cadence, anything else would flood the
// runs table and the log with empty ticks.
func (s *Scheduler) ReviewCycle(ctx context.Context) error {
	return s.reviewCycle(ctx, ctx)
}

func (s *Scheduler) reviewCycle(stopCtx, reviewCtx context.Context) error {
	// Usage floor: leave headroom in the Codex windows for interactive work.
	// Checked before the run-lock so a paused cycle records no run. The loop
	// keeps ticking, so reviews resume as soon as the window refills.
	select {
	case <-stopCtx.Done():
		return nil
	default:
	}
	cfg := s.cfg()
	if paused, reason := usage.BelowFloor(s.usageFn(), cfg.UsageFloor5h(), cfg.UsageFloorWeekly()); paused {
		s.logf("cycle: paused by usage floor (%s)", reason)
		return nil
	}

	staleAfter := cfg.LeaseWindow()
	queue, err := s.store.ListQueue(reviewCtx, "")
	if err != nil {
		return err
	}
	available := availableCandidates(queue, time.Now(), staleAfter)
	if len(available) == 0 {
		return nil
	}

	engine, err := s.newEngine(cfg)
	if err != nil {
		return fmt.Errorf("build review engine: %w", err)
	}

	s.logf("cycle: started at %s", time.Now().Format(time.RFC3339))

	if _, active, err := s.store.ActiveRun(reviewCtx, staleAfter); err != nil {
		return err
	} else if active {
		s.logf("cycle: a previous run is still active — skipping")
		return nil
	}

	run := store.Run{ID: newRunID(), StartedAt: time.Now(), Host: hostname(), PID: os.Getpid()}
	if err := s.store.StartRun(reviewCtx, run); err != nil {
		return err
	}
	status := "done"
	defer func() {
		if err := s.store.FinishRun(reviewCtx, run.ID, status); err != nil {
			s.logf("cycle: finish run: %v", err)
		}
		s.logf("cycle: finished at %s (%s)", time.Now().Format(time.RFC3339), status)
	}()

	s.logf("cycle: %d candidate(s) to review", len(available))
	s.processQueue(stopCtx, reviewCtx, available, cfg, engine)
	return nil
}

// availableCandidates filters the queue to rows that are actually reviewable
// right now: no live lease (see store.Candidate.ClaimActive — a fresh claim
// is another worker mid-review; a stale one is a crashed daemon's abandoned
// lease, reclaimed here) and no eligibility hold (store.Candidate.Held —
// cooling down after a recent review, or settling after a fresh push). Pure —
// the boundary is unit-tested directly.
func availableCandidates(queue []store.Candidate, now time.Time, staleAfter time.Duration) []store.Candidate {
	out := make([]store.Candidate, 0, len(queue))
	for _, c := range queue {
		if !c.ClaimActive(now, staleAfter) && !c.Held(now) {
			out = append(out, c)
		}
	}
	return out
}

// RunCycle is the one-shot flow (`run --once`): reconcile leftovers, then a
// discovery sweep followed by one review cycle.
func (s *Scheduler) RunCycle(ctx context.Context) error {
	if err := s.Reconcile(ctx); err != nil {
		s.logf("reconcile: %v", err)
	}
	if err := s.Discover(ctx); err != nil {
		return err
	}
	return s.ReviewCycle(ctx)
}

// Reconcile cleans up after crashed processes on THIS host: run rows still
// marked running and queue claims whose recorded pid is dead are released
// immediately instead of waiting out the lease window (2h+ of "a previous
// run is still active — skipping" after every mid-cycle crash, which bites
// hardest during development). Another host's state — and any live pid's —
// is left strictly alone: a sibling instance's in-flight work looks exactly
// like this, minus the dead pid.
func (s *Scheduler) Reconcile(ctx context.Context) error {
	host := hostname()

	runs, err := s.store.RunningRuns(ctx)
	if err != nil {
		return err
	}
	for _, r := range runs {
		if r.Host != host || s.pidAlive(r.PID) {
			continue
		}
		s.logf("reconcile: run %s (pid %d) died mid-cycle — marking failed", r.ID, r.PID)
		if err := s.store.FinishRun(ctx, r.ID, "failed"); err != nil {
			return err
		}
	}

	queue, err := s.store.ListQueue(ctx, "")
	if err != nil {
		return err
	}
	for _, c := range queue {
		if c.ClaimedAt == nil || c.ClaimHost != host || s.pidAlive(c.ClaimPID) {
			continue
		}
		s.logf("reconcile: %s#%d was claimed by dead pid %d — releasing", c.Repo, c.Number, c.ClaimPID)
		if err := s.store.ClearClaim(ctx, c.Repo, c.Number); err != nil {
			return err
		}
	}
	return nil
}

// pidAlive is the production liveness probe: signal 0 reaches any process we
// can address. EPERM means "alive but not ours" — still alive. Non-positive
// pids (missing data) count as dead rather than blocking reconciliation.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

// processQueue reviews candidates concurrently, capped at cfg.MaxParallel.
// The input is already sorted New-before-Refreshed, oldest-first by the
// store. The cycle's config snapshot and engine travel as parameters so
// every goroutine works from one coherent config — nothing cycle-scoped
// lives on the long-lived Scheduler struct.
func (s *Scheduler) processQueue(stopCtx, reviewCtx context.Context, candidates []store.Candidate, cfg config.Config, engine review.Engine) {
	sem := make(chan struct{}, cfg.MaxParallel())
	var wg sync.WaitGroup
	for _, c := range candidates {
		select {
		case <-stopCtx.Done():
			s.logf("cycle: shutdown requested — waiting for in-flight reviewer(s)")
			wg.Wait()
			return
		default:
		}
		select {
		case sem <- struct{}{}:
		case <-stopCtx.Done():
			s.logf("cycle: shutdown requested — waiting for in-flight reviewer(s)")
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
// check — explicit re-review requests and draft reviews must always go
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
	s.logf("review %s#%d: no longer a candidate (%s) — recording skip", c.Repo, c.Number, reason)
	return true, s.store.Complete(ctx, store.ReviewFrom(c, review.DecisionSkipped, store.EnginePrecheck, started))
}

// reviewOne claims a candidate, rechecks its candidacy, runs the engine, and
// completes it: every outcome — including SKIPPED and ERROR — is recorded in
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
		s.logf("review %s#%d: claimed by another worker — skipping", c.Repo, c.Number)
		_ = os.Remove(workDir)
		return nil
	}
	if skipped, err := s.skipIfStale(ctx, c, claimedAt); skipped || err != nil {
		return err
	}
	// Leave the tmp dir in place — a future run may reuse it (per the spec).

	allowed, err := s.store.IsAuthorAllowed(ctx, c.Repo, c.Author)
	if err != nil {
		return err
	}
	facts := review.DeriveFacts(c, s.ghUser, allowed)
	prompt := review.BuildPrompt(cfg, c, facts)

	verdict, reviewErr := engine.Review(ctx, review.Request{Candidate: c, Prompt: prompt, WorkDir: workDir})
	if verdict.Summary != "" {
		s.logf("review %s#%d: %s — %s", c.Repo, c.Number, verdict.Decision, verdict.Summary)
	}
	// A failed invocation's only clue is the engine's own output — surface its
	// tail instead of a bare exit status.
	if reviewErr != nil && verdict.Raw != "" {
		s.logf("review %s#%d: engine output tail: %s", c.Repo, c.Number, tail(verdict.Raw, 500))
	}

	// Every outcome goes to history — SKIPPED/ERROR included. They don't
	// block a future re-review: store.LastReview filters them out of
	// Refreshed detection, and new commits change the SHA that discovery's
	// same-SHA suppression keys on.
	rec := store.ReviewFrom(c, verdict.Decision, engine.Name(), claimedAt)
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

func newRunID() string { return fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid()) }

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}
