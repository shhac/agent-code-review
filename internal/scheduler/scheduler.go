// Package scheduler owns the deterministic review cycle: take the run-lock,
// discover candidates, process the queue oldest-first up to the parallelism
// cap, record verdicts, release the lock. The serve daemon runs Discover and
// ReviewCycle as independent heartbeat loops (Start); `run --once` calls
// RunCycle a single time.
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
	// loopRunner and reconcile are narrow lifecycle seams: production uses the
	// real methods, while tests can assert StartGraceful's orchestration without
	// starting timers or requiring a fully populated Store.
	loopRunner func(context.Context, func() time.Duration, string, func(context.Context) error)
	reconcile  func(context.Context) error
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
		loopRunner:     nil,
		reconcile:      nil,
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
