// Package scheduler owns the deterministic review cycle: take the run-lock,
// discover candidates, process the queue oldest-first up to the parallelism
// cap, record verdicts, release the lock. The serve daemon runs the
// discovery and review heartbeat loops via StartGraceful; `run --once`
// calls RunCycle a single time.
package scheduler

import (
	"context"
	"sync/atomic"
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
// getter, so the fail-open rule lives in exactly one place:
// usage.BelowFloor, which never pauses on an empty snapshot.
type UsageFn func() usage.Snapshot

// SchedulerStore is the subset of persistence the scheduler owns. Keeping it
// here makes scheduler tests declare only the effects they exercise instead
// of depending on the application's whole storage surface.
type SchedulerStore interface {
	ListQueue(context.Context, string) ([]store.Candidate, error)
	Claim(context.Context, string, int, store.Lease) (bool, error)
	ClearClaim(context.Context, string, int) error
	AppendHistory(context.Context, store.Review) error
	Complete(context.Context, store.Review) error
	IsAuthorAllowed(context.Context, string, string) (bool, error)
	ActiveRun(context.Context, time.Duration) (store.Run, bool, error)
	RunningRuns(context.Context) ([]store.Run, error)
	StartRun(context.Context, store.Run) error
	FinishRun(context.Context, string, string) error
}

// Scheduler wires the deterministic machinery around a review engine. Config
// comes through a getter so edits to config.json (cadence, parallelism,
// usage floors, codex settings) apply without a restart; only the loop
// on/off switches are fixed at boot, because the --no-* flags own them.
type Scheduler struct {
	cfg         func() config.Config
	store       SchedulerStore
	disc        *discover.Discoverer
	ghUser      string
	logf        Logf
	usageFn     UsageFn
	discovering atomic.Bool // in-flight guard for the discovery sweep

	// priceFn values a finished review's token classes at its model's rates,
	// for the engine that reports no cost of its own. nil, or a false second
	// result, means no estimate is possible — which must record as "unknown"
	// rather than as a free review.
	priceFn PriceFn

	// newEngine builds the review engine from live config at the start of
	// each cycle, so codex.* edits apply without a restart.
	newEngine func(config.Config) (review.Engine, error)
	// stillCandidate re-checks a PR's candidacy just before the engine runs
	// (discover.StillCandidate in production; swapped in tests).
	stillCandidate func(ctx context.Context, repo string, number int, login, head string) (bool, string, error)
	// pidAlive reports whether a pid is a live process on THIS host (real
	// signal-0 probe in production; swapped in tests). Reconcile uses it to
	// tell a crashed daemon's leftovers from a sibling instance's live work.
	pidAlive func(pid int) bool
	// loopRunner and reconcile are narrow lifecycle seams: production uses the
	// real methods, while tests can assert StartGraceful's orchestration without
	// starting timers or requiring a fully populated Store.
	loopRunner func(context.Context, func() time.Duration, string, func(context.Context) error)
	reconcile  func(context.Context) error
	// heartbeat is loop's interval-re-read cadence (loopHeartbeat in
	// production; shrunk by tests so the loop is drivable without real waits).
	heartbeat time.Duration
}

// PriceFn values one review's token classes in USD. The second result is
// false when the model is unknown to the price table or the row carries no
// class split, both of which mean "cannot estimate", never "free".
type PriceFn func(model string, input, output, cacheWrite, cacheRead int) (float64, bool)

// WithPricing attaches the estimator. Optional by design: a daemon that has
// never reached the price source reviews exactly as before, recording no
// estimate rather than failing.
func (s *Scheduler) WithPricing(fn PriceFn) *Scheduler {
	s.priceFn = fn
	return s
}

func New(cfg func() config.Config, s SchedulerStore, d *discover.Discoverer, ghUser string, logf Logf, usageFn UsageFn) *Scheduler {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if usageFn == nil {
		usageFn = func() usage.Snapshot { return usage.Snapshot{} }
	}
	sched := &Scheduler{
		cfg: cfg, store: s, disc: d, ghUser: ghUser,
		logf: logf, usageFn: usageFn,
		newEngine:      func(c config.Config) (review.Engine, error) { return review.NewEngine(c.Review) },
		stillCandidate: discover.StillCandidateAt,
		pidAlive:       pidAlive,
		heartbeat:      loopHeartbeat,
	}
	// Method-valued seams can't appear in the literal above; same convention
	// as the other seams: production impls at construction, tests overwrite.
	sched.loopRunner = sched.loop
	sched.reconcile = sched.Reconcile
	return sched
}

// Discover scrapes the watched repos for candidates. Purely deterministic:
// gh + classification rules, no LLM involved. A guard skips the sweep when
// the previous one is still in flight.
func (s *Scheduler) Discover(ctx context.Context) error {
	if !s.discovering.CompareAndSwap(false, true) {
		s.logf("discover: previous sweep still running, skipping")
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
