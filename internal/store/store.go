// Package store persists the review queue, review history, and run-lock. The
// only backend today is DuckDB via the `duckdb` CLI (subprocess, CGO-free —
// see duckdb.go), chosen so the binary still cross-compiles through the family
// release pipeline. Everything is behind the Store interface so a second
// backend (or a future embedded driver) can drop in.
package store

import (
	"context"
	"fmt"
	"time"
)

// Candidate is a PR in the review queue. A candidate exists exactly while
// review work is pending — completion moves it into history.
type Candidate struct {
	Repo         string     `json:"repo"`
	Number       int        `json:"number"`
	Type         string     `json:"type"` // "new" | "refreshed"
	Title        string     `json:"title"`
	Author       string     `json:"author"`
	URL          string     `json:"url"`
	HeadSHA      string     `json:"head_sha"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	QueuePos     int        `json:"queue_pos"`
	DiscoveredAt time.Time  `json:"discovered_at"`         // first time discovery saw this pending work; never bumped by later sweeps
	ClaimedAt    *time.Time `json:"claimed_at,omitempty"`  // set while an engine reviews it; stale claims are reclaimable
	Source       string     `json:"source"`                // SourceDiscovered | SourceManual
	WorkDir      string     `json:"work_dir,omitempty"`    // engine scratch workspace, set at claim time; <work_dir>/agent.log is the live review log
	EligibleAt   *time.Time `json:"eligible_at,omitempty"` // eligibility hold: the scheduler skips this row until then; nil = eligible now
	HoldReason   string     `json:"hold_reason,omitempty"` // HoldCooldown | HoldSettling while a hold is set
}

// Hold reasons: why a queued candidate is not yet eligible for review.
const (
	HoldCooldown = "cooldown" // we reviewed this PR recently (candidates.rereview_cooldown)
	HoldSettling = "settling" // the PR was updated too recently (candidates.quiet_period)
)

// Candidate sources. Manual adds bypass the pre-review candidacy check so
// explicit re-review requests and draft reviews always go through.
const (
	SourceDiscovered = "discovered"
	SourceManual     = "manual"
)

// Synthetic engine markers: history rows produced without invoking a review
// engine record their provenance in the engine column instead.
const (
	EnginePrecheck = "precheck" // scheduler's pre-review candidacy recheck
	EngineManual   = "manual"   // `queue skip` by a human
)

// ReviewFrom snapshots a candidate's identity into a history record — the
// single place the Candidate→Review field fan-out lives, so a new snapshot
// field cannot be added to one Complete call site and missed at another.
// started is when the review began (the claim time); the zero value records
// an unknown duration as 0 (manual skips, backfilled rows).
func ReviewFrom(c Candidate, verdict, engine string, started time.Time) Review {
	duration := 0
	if !started.IsZero() {
		duration = int(time.Since(started).Seconds())
	}
	return Review{
		Repo:         c.Repo,
		Number:       c.Number,
		Title:        c.Title,
		Author:       c.Author,
		HeadSHA:      c.HeadSHA,
		Verdict:      verdict,
		Engine:       engine,
		ReviewedAt:   time.Now(),
		DurationSecs: duration,
		WorkDir:      c.WorkDir,
	}
}

// ClaimActive reports whether c's claim is a live lease: an engine claimed it
// within the window. False for unclaimed rows and for stale claims (a crashed
// daemon's leftovers — eligible for reclaim). This is THE lease predicate:
// the scheduler's reclaim filter and the dashboard's "reviewing" badge are
// both defined in terms of it, so they cannot disagree.
func (c Candidate) ClaimActive(now time.Time, window time.Duration) bool {
	return c.ClaimedAt != nil && now.Sub(*c.ClaimedAt) <= window
}

// Held reports whether c is under an eligibility hold: queued, visible, but
// not yet reviewable. THE hold predicate — the scheduler's eligibility filter
// and the dashboard's "on hold" badge are both defined in terms of it, so
// they cannot disagree.
func (c Candidate) Held(now time.Time) bool {
	return c.EligibleAt != nil && now.Before(*c.EligibleAt)
}

// Review records one completed outcome for a PR at a specific head SHA —
// including SKIPPED and ERROR, which live in history like everything else.
// Title and Author are snapshots of the PR at completion time so the History
// page can render outcomes like queue items without a gh round-trip.
type Review struct {
	Repo         string    `json:"repo"`
	Number       int       `json:"number"`
	Title        string    `json:"title"`
	Author       string    `json:"author"`
	HeadSHA      string    `json:"head_sha"`
	Verdict      string    `json:"verdict"` // APPROVED|COMMENTED|REQUESTED_CHANGES|SKIPPED|ERROR
	Engine       string    `json:"engine"`
	ReviewedAt   time.Time `json:"reviewed_at"`
	DurationSecs int       `json:"duration_secs"`      // claim-to-completion elapsed; 0 when unknown
	WorkDir      string    `json:"work_dir,omitempty"` // engine workspace used, kept for postmortem log access
	TokensUsed   int       `json:"tokens_used"`        // engine-reported token spend; 0 when unknown
}

// Workspace is where a PR's review agent ran, resolved by FindWorkspace.
// Exactly one of Queued/Finished is set: Queued while the PR still has a
// queue row (in-flight review or reclaimable claim), Finished for a
// postmortem from history.
type Workspace struct {
	Dir      string
	Queued   *Candidate
	Finished *Review
}

// FindWorkspace resolves a PR's recorded engine workspace: the live queue
// row first, then the most recent history row. false means no workspace was
// ever recorded (reviews predating workdir tracking have none). The CLI's
// `queue log` and the dashboard's review-log endpoint share this resolution
// so the two surfaces cannot drift.
func FindWorkspace(ctx context.Context, s Store, repo string, number int) (Workspace, bool, error) {
	queue, err := s.ListQueue(ctx, repo)
	if err != nil {
		return Workspace{}, false, err
	}
	for _, c := range queue {
		if c.Number == number && c.WorkDir != "" {
			return Workspace{Dir: c.WorkDir, Queued: &c}, true, nil
		}
	}
	last, ok, err := s.LastOutcome(ctx, repo, number)
	if err != nil {
		return Workspace{}, false, err
	}
	if ok && last.WorkDir != "" {
		return Workspace{Dir: last.WorkDir, Finished: &last}, true, nil
	}
	return Workspace{}, false, nil
}

// realVerdicts is the single source of the "actual posted review" set — the
// outcomes that count as "reviewed at this SHA" for Refreshed detection.
// SKIPPED and ERROR deliberately aren't in it: new commits (or a manual
// re-add) must be able to re-surface those PRs. Both IsRealVerdict and the
// driver's SQL filter derive from this list. (The engine's Decision*
// constants in the review package mirror these strings; review imports
// store, so the vocabulary can't reference them from here.)
var realVerdicts = []string{"APPROVED", "COMMENTED", "REQUESTED_CHANGES"}

// IsRealVerdict reports whether v is an actual posted review — the predicate
// behind LastReview's history filter.
func IsRealVerdict(v string) bool {
	for _, rv := range realVerdicts {
		if v == rv {
			return true
		}
	}
	return false
}

// AllowedAuthor is one entry in a repo's allowed-authors list: an author whose
// PRs WE may approve (we are the reviewer — this is not about who can approve).
// Decided per repo: a PR may be approved only if its author is listed for that
// PR's repo (or for the wildcard repo "*"). The list lives in the store, not
// config, so it can be managed at runtime and vary per repo.
type AllowedAuthor struct {
	Repo         string `json:"repo"`
	GitHubHandle string `json:"github_handle"`
	Name         string `json:"name,omitempty"`
	Email        string `json:"email,omitempty"`
	SlackID      string `json:"slack_id,omitempty"`
}

// WildcardRepo as an AllowedAuthor.Repo applies the entry to every repo.
const WildcardRepo = "*"

// Run is one review cycle, used as the advisory run-lock.
type Run struct {
	ID         string     `json:"id"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Status     string     `json:"status"` // running|done|failed
	Host       string     `json:"host"`
	PID        int        `json:"pid"`
}

// Candidate types.
const (
	TypeNew       = "new"
	TypeRefreshed = "refreshed"
)

// Store is the persistence contract.
type Store interface {
	// Init applies the schema (idempotent).
	Init(ctx context.Context) error

	// Enqueue inserts c, or — when the PR is already queued — refreshes its
	// discovered metadata (type, title, author, url, head_sha, updated_at).
	// discovered_at keeps its first-seen value, and it never touches
	// claimed_at or queue_pos, so it cannot stomp an in-flight review or a
	// manual reorder. The eligibility hold (eligible_at/hold_reason) only
	// ever extends: a sweep can push eligibility later (the author is still
	// active) but never earlier; a manual-source enqueue clears it.
	Enqueue(ctx context.Context, c Candidate) error
	// ListQueue returns the whole queue in scheduler order: queue_pos, then
	// FIFO on first discovery (oldest discovered_at first — later sweeps
	// never leapfrog waiting work), with new-before-refreshed and PR number
	// as same-instant tiebreaks. repo narrows to one repo; "" means all.
	ListQueue(ctx context.Context, repo string) ([]Candidate, error)
	// Claim marks a candidate as being reviewed right now and records the
	// engine's scratch workspace (whose agent.log is the live review log).
	// Claims are advisory leases: a claim older than the caller's staleness
	// window is treated as abandoned (crashed daemon) and reclaimed.
	Claim(ctx context.Context, repo string, number int, at time.Time, workDir string) error
	// Complete records r in history and removes the queue row — atomically,
	// in one store round-trip. The delete is gated on r.HeadSHA: if the row's
	// head has advanced while the review ran, the row survives (its claim is
	// cleared instead) so the newer commits get reviewed next cycle.
	Complete(ctx context.Context, r Review) error
	// Dequeue drops a candidate without recording an outcome — the "changed
	// our mind" path.
	Dequeue(ctx context.Context, repo string, number int) error
	SetQueuePos(ctx context.Context, repo string, number int, pos int) error
	// Promote is the "review this now" action: float the row to the top of
	// the queue, clear any eligibility hold, and escalate source to manual —
	// equivalent to a manual add, so the pre-review candidacy check is
	// bypassed too.
	Promote(ctx context.Context, repo string, number int) error

	// LastReview returns the most recent REAL review (per IsRealVerdict) for
	// a PR, if any. SKIPPED/ERROR rows never count as "reviewed at this SHA",
	// or Refreshed detection could never re-surface those PRs.
	LastReview(ctx context.Context, repo string, number int) (Review, bool, error)
	// LastOutcome returns the most recent history row of ANY verdict for a
	// PR, if any — the input to same-SHA re-enqueue suppression.
	LastOutcome(ctx context.Context, repo string, number int) (Review, bool, error)
	// ListReviews returns outcome history, most recent first, capped at limit.
	ListReviews(ctx context.Context, limit int) ([]Review, error)
	// ListReviewsSince returns all outcomes at or after since, oldest first.
	ListReviewsSince(ctx context.Context, since time.Time) ([]Review, error)
	// TokensUsed sums the engine-reported token spend of outcomes at or
	// after since; the zero time sums all history.
	TokensUsed(ctx context.Context, since time.Time) (int64, error)

	// Allowed authors (per repo, "*" = all repos): whose PRs we may approve.
	AllowAuthor(ctx context.Context, a AllowedAuthor) error
	DenyAuthor(ctx context.Context, repo, handle string) error
	ListAllowedAuthors(ctx context.Context, repo string) ([]AllowedAuthor, error)
	// IsAuthorAllowed reports whether handle's PRs may be approved for repo,
	// matching the repo exactly or the wildcard "*".
	IsAuthorAllowed(ctx context.Context, repo, handle string) (bool, error)

	// ActiveRun returns an unfinished run more recent than staleAfter, if any.
	ActiveRun(ctx context.Context, staleAfter time.Duration) (Run, bool, error)
	StartRun(ctx context.Context, r Run) error
	FinishRun(ctx context.Context, id string, status string) error
	// ListRuns returns cycle history, most recent first, capped at limit.
	ListRuns(ctx context.Context, limit int) ([]Run, error)

	Close() error
}

// Open returns a Store for the given engine + path. Only "duckdb" (and its
// empty-string default) is wired today.
func Open(engine, path string) (Store, error) {
	switch engine {
	case "", "duckdb":
		return newDuckDB(path)
	default:
		return nil, fmt.Errorf("Unknown store engine: %q. Valid: duckdb", engine)
	}
}
