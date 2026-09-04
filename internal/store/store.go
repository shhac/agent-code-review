// Package store persists the review queue, review history, and run-lock. The
// only backend today is DuckDB via the `duckdb` CLI (subprocess, CGO-free;
// see duckdb.go), chosen so the binary still cross-compiles through the family
// release pipeline. Everything is behind the Store interface so a second
// backend (or a future embedded driver) can drop in.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
)

// Store is the persistence contract.
type Store interface {
	// Init applies the schema (idempotent).
	Init(ctx context.Context) error

	// Enqueue inserts c, or, when the PR is already queued, refreshes its
	// discovered metadata (type, title, author, url, head_sha, updated_at).
	// discovered_at keeps its first-seen value, and it never touches
	// claimed_at or queue_pos, so it cannot stomp an in-flight review or a
	// manual reorder. The eligibility hold (eligible_at/hold_reason) only
	// ever extends: a sweep can push eligibility later (the author is still
	// active) but never earlier; a manual-source enqueue clears it.
	Enqueue(ctx context.Context, c Candidate) error
	// ListQueue returns the whole queue in scheduler order: queue_pos, then
	// FIFO on first discovery (oldest discovered_at first; later sweeps
	// never leapfrog waiting work), with new-before-refreshed and PR number
	// as same-instant tiebreaks. repo narrows to one repo; "" means all.
	ListQueue(ctx context.Context, repo string) ([]Candidate, error)
	// Claim marks a candidate as being reviewed right now and records the
	// engine's scratch workspace (whose agent.log is the live review log)
	// plus the claimer's host+pid. Compare-and-swap: it succeeds only when
	// the row is unclaimed or the existing claim is older than l.StaleAfter
	// (abandoned by a crashed daemon), so two workers, including two daemon
	// instances, can never both win the same row. false = another worker
	// holds a live claim; skip the row.
	Claim(ctx context.Context, repo string, number int, l Lease) (bool, error)
	// ClearClaim releases a claim without recording an outcome; boot
	// reconciliation uses it to free rows abandoned by a dead process.
	ClearClaim(ctx context.Context, repo string, number int) error
	// Complete records r in history and removes the queue row, atomically,
	// in one store round-trip. The delete is gated on r.HeadSHA: if the row's
	// head has advanced while the review ran, the row survives (its claim is
	// cleared instead) so the newer commits get reviewed next cycle.
	Complete(ctx context.Context, r Review) error
	// Dequeue drops a candidate without recording an outcome: the "changed
	// our mind" path.
	Dequeue(ctx context.Context, repo string, number int) error
	// Reorder updates every supplied queue position atomically. Dashboard drag
	// requests are validated against the current queue before reaching here;
	// one statement prevents a failed request from leaving a partial ordering.
	Reorder(ctx context.Context, positions []QueuePosition) error
	// Promote is the "review this now" action: float the row to the top of
	// the queue, clear any eligibility hold, and escalate source to manual:
	// equivalent to a manual add, so the pre-review candidacy check is
	// bypassed too.
	Promote(ctx context.Context, repo string, number int) error

	// LastReview returns the most recent REAL review (per IsRealVerdict) for
	// a PR, if any. SKIPPED/ERROR rows never count as "reviewed at this SHA",
	// or Refreshed detection could never re-surface those PRs.
	LastReview(ctx context.Context, repo string, number int) (Review, bool, error)
	// LastOutcome returns the most recent history row of ANY verdict for a
	// PR, if any: the input to same-SHA re-enqueue suppression.
	LastOutcome(ctx context.Context, repo string, number int) (Review, bool, error)
	// ListReviews returns outcome history, most recent first, capped at limit.
	ListReviews(ctx context.Context, limit int) ([]Review, error)
	// ReviewByLogKey returns one exact history row by its ReviewLogKey.
	ReviewByLogKey(ctx context.Context, repo string, number int, logKey string) (Review, bool, error)
	// ListReviewsSince returns all outcomes at or after since, oldest first.
	ListReviewsSince(ctx context.Context, since time.Time) ([]Review, error)
	// FreshTokens sums the comparable (cache-excluded) token spend of
	// outcomes at or after since; the zero time sums all history. Not
	// tokens_used: that column's meaning depends on which engine wrote it,
	// so summing it across a mixed history compares two measurements.
	FreshTokens(ctx context.Context, since time.Time) (int64, error)
	// UnpricedModels and EstimateCosts fill in our own valuation for rows
	// that were completed before it could be computed (no price table
	// reachable, or a build that recorded the token split before there was
	// anywhere to record a valuation). Gap-filling only: a row that already
	// has an estimate keeps it, so a recorded cost stays the cost at the time.
	UnpricedModels(ctx context.Context) ([]string, error)
	EstimateCosts(ctx context.Context, rates map[string]CostRates) (int64, error)
	// AppendHistory records an outcome without retiring the queue row, for an
	// attempt that ended with work still pending (a daemon killed mid-review).
	AppendHistory(ctx context.Context, r Review) error

	// Author roster (per repo, "*" = all repos): which group an author is in.
	// The group names a policy in config; see config.ResolvePolicy.
	SetAuthorGroup(ctx context.Context, a Author) error
	RemoveAuthor(ctx context.Context, repo, handle string) error
	// ListAuthors returns roster rows, narrowed by repo and/or group; "" means
	// don't narrow on that field.
	ListAuthors(ctx context.Context, repo, group string) ([]Author, error)
	// SetSteering records the instruction shaping the next review of a PR,
	// replacing any previous one. ClearSteering drops it; Complete drops it
	// too, since steering guides one review rather than every future one.
	SetSteering(ctx context.Context, st Steering) error
	ClearSteering(ctx context.Context, repo string, number int) error
	Steering(ctx context.Context, repo string, number int) (Steering, bool, error)
	ListSteering(ctx context.Context) ([]Steering, error)
	// AuthorByTailscaleLogin resolves a Tailscale-User-Login header value to
	// the roster row claiming it, which is how the dashboard learns which
	// GitHub handle a tailnet viewer is.
	AuthorByTailscaleLogin(ctx context.Context, login string) (Author, bool, error)
	// AuthorGroup returns the membership that applies to handle on repo: the
	// repo's own row if there is one, else the wildcard row, else the zero
	// value (unlisted, so config's unlisted fallback decides).
	AuthorGroup(ctx context.Context, repo, handle string) (config.Membership, error)

	Close() error
}

// Open returns a Store for the given engine + path. Only "duckdb" (and its
// empty-string default) is wired today.
func Open(engine, path string) (Store, error) {
	return open(engine, path, false)
}

// OpenReadOnly returns a Store that can only read: it attaches to an existing
// DB without applying the schema, and refuses every write. Use it to inspect
// the live daemon's data without risking a mutation or a lock fight.
func OpenReadOnly(engine, path string) (Store, error) {
	return open(engine, path, true)
}

func open(engine, path string, readOnly bool) (Store, error) {
	switch engine {
	case "", "duckdb":
		return newDuckDB(path, readOnly)
	default:
		return nil, fmt.Errorf("Unknown store engine: %q. Valid: duckdb", engine)
	}
}
