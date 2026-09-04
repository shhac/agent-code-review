// Package scheduler owns the deterministic half of a review: discover
// candidates, consume the queue oldest-first up to the parallelism cap,
// record verdicts. Reviews are dispatched one at a time as slots free, so a
// PR that becomes ready while others are in flight is picked up by the next
// free slot rather than waiting for a batch to drain. Cross-process exclusion
// is the per-candidate claim CAS in store.Claim, not a global lock.
//
// The serve daemon runs the discovery loop and the review dispatcher via
// StartGraceful; `run` calls RunOnce, which drains the queue and exits.
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

// UsageFn supplies the latest usage snapshot for ONE engine. It takes the
// engine name because concurrent reviews can run several: an author's group picks
// the engine that reviews their PRs, so headroom has to be asked about per
// engine rather than once for the configured one. Callers with no usage data
// (one-shot runs) pass nil; New normalizes that to an empty-snapshot getter,
// so the fail-open rule lives in exactly one place: usage.BelowFloor, which
// never pauses on an empty snapshot.
type UsageFn func(engine string) usage.Snapshot

// Stop carries the daemon's two shutdown contexts as one value. They are both
// context.Context and mean opposite things — Graceful stops NEW work while
// in-flight reviews finish, Force ends the reviews themselves — so passing
// them as two positional parameters made swapping them a silent compile. The
// old code's defence was a comment noting this was "the one code path where
// confusing them kills a review mid-flight"; a type removes the hazard
// instead of documenting it.
type Stop struct {
	Graceful context.Context
	Force    context.Context
}

// Stopping reports whether either context has ended.
func (st Stop) Stopping() bool { return st.Graceful.Err() != nil || st.Force.Err() != nil }

// Sweeper scrapes the watched repos for candidate PRs. Named on the consumer
// side, as SchedulerStore is (and as discover.candidateStore and
// dashboard.dashboardStore are), so the scheduler depends on the one method it
// calls rather than on *discover.Discoverer. That concrete pointer was the
// package's only unfaked dependency, and it is what forced the loop lifecycle
// to be testable only by patching the Scheduler's own methods.
type Sweeper interface {
	Discover(ctx context.Context) ([]store.Candidate, error)
}

// SchedulerStore is the subset of persistence the scheduler owns. Keeping it
// here makes scheduler tests declare only the effects they exercise instead
// of depending on the application's whole storage surface.
type SchedulerStore interface {
	ListQueue(context.Context, string) ([]store.Candidate, error)
	Claim(context.Context, string, int, store.Lease) (bool, error)
	ClearClaim(context.Context, string, int) error
	AppendHistory(context.Context, store.Review) error
	Complete(context.Context, store.Review) error
	AuthorGroup(context.Context, string, string) (config.Membership, error)
	Steering(ctx context.Context, repo string, number int) (store.Steering, bool, error)
}

// Scheduler wires the deterministic machinery around a review engine. Config
// comes through a getter so edits to config.json (cadence, parallelism,
// usage floors, codex settings) apply without a restart; only the loop
// on/off switches are fixed at boot, because the --no-* flags own them.
//
// Every field is set by New from Deps and never written afterwards.
type Scheduler struct {
	cfg         func() config.Config
	store       SchedulerStore
	sweeper     Sweeper
	ghUser      string
	logf        Logf
	usageFn     UsageFn
	discovering atomic.Bool // in-flight guard for the discovery sweep

	// priceFn values a finished review's token classes at its model's rates,
	// for the engine that reports no cost of its own. nil, or a false second
	// result, means no estimate is possible — which must record as "unknown"
	// rather than as a free review.
	priceFn PriceFn

	// Injected collaborators; see Deps for what each stands in for.
	newEngine      EngineFactory
	stillCandidate CandidacyFn
	pidAlive       LivenessFn
	heartbeat      time.Duration
	now            func() time.Time
}

// PriceFn values one review's token classes in USD. The second result is
// false when the model is unknown to the price table or the row carries no
// class split, both of which mean "cannot estimate", never "free".
//
// It takes the whole TokenUsage rather than four positional ints: the classes
// are priced at very different rates, so transposing cache-write and
// cache-read mis-values a review silently and by a large factor. The caller
// has the struct in hand already.
type PriceFn func(model string, t review.TokenUsage) (float64, bool)

// EngineFactory builds the review engine for ONE candidate, from live config
// patched with that author's policy: codex.* edits apply without a restart,
// and a group naming its own engine/model/effort gets it.
type EngineFactory func(config.Config, config.Policy) (review.Engine, error)

// CandidacyFn re-checks a PR's candidacy just before the engine spend, so a PR
// approved, merged or closed while it waited in the queue is not reviewed.
type CandidacyFn func(ctx context.Context, repo string, number int, login, head string) (bool, string, error)

// LivenessFn reports whether a pid is a live process on THIS host. Reconcile
// uses it to tell a crashed daemon's leftovers from a sibling instance's
// in-flight work, which look identical apart from the pid.
type LivenessFn func(pid int) bool

// Deps is everything a Scheduler is built from. A struct rather than a
// positional argument list because most fields have a production default and
// only tests set the rest: a caller supplies what it cares about and New fills
// the remainder, so a new seam does not churn every call site.
//
// Store, Config and Sweeper are required. The rest default to the production
// implementations.
type Deps struct {
	Store   SchedulerStore
	Config  func() config.Config
	Sweeper Sweeper
	GHUser  string
	Logf    Logf
	Usage   UsageFn
	Price   PriceFn

	// Seams. Each stands in for a real external collaborator, so each is a
	// named func type rather than a one-method interface: that is Go's
	// idiomatic shape for a single-method dependency and needs no adapter.
	NewEngine      EngineFactory
	StillCandidate CandidacyFn
	PIDAlive       LivenessFn
	Heartbeat      time.Duration
	Now            func() time.Time
}

// New builds a Scheduler from d, filling every unset optional field with its
// production implementation.
func New(d Deps) *Scheduler {
	if d.Logf == nil {
		d.Logf = func(string, ...any) {}
	}
	// The fail-open rule for missing usage data lives in exactly one place:
	// usage.BelowFloor, which never pauses on an empty snapshot.
	if d.Usage == nil {
		d.Usage = func(string) usage.Snapshot { return usage.Snapshot{} }
	}
	if d.NewEngine == nil {
		d.NewEngine = func(c config.Config, p config.Policy) (review.Engine, error) {
			return review.NewEngine(c.Review.WithPolicy(p))
		}
	}
	if d.StillCandidate == nil {
		d.StillCandidate = discover.StillCandidateAt
	}
	if d.PIDAlive == nil {
		d.PIDAlive = pidAlive
	}
	if d.Heartbeat == 0 {
		d.Heartbeat = loopHeartbeat
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Scheduler{
		cfg: d.Config, store: d.Store, sweeper: d.Sweeper, ghUser: d.GHUser,
		logf: d.Logf, usageFn: d.Usage, priceFn: d.Price,
		newEngine: d.NewEngine, stillCandidate: d.StillCandidate, pidAlive: d.PIDAlive,
		heartbeat: d.Heartbeat, now: d.Now,
	}
}
