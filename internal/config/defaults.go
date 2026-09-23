package config

// Resolved getters: apply defaults so callers never special-case zero. The raw
// schema lives in schema.go; these read it and fill in the effective value.

import (
	"path/filepath"
	"time"

	"github.com/shhac/lib-agent-cli/xdg"
)

// NewMaxAge is the New-candidate age window (default 14 days).
func (c Config) NewMaxAge() time.Duration {
	return daysOr(c.Candidates.NewMaxAgeDays, 14)
}

// RefreshedMaxAge is the Refreshed-candidate age window (default 21 days).
func (c Config) RefreshedMaxAge() time.Duration {
	return daysOr(c.Candidates.RefreshedMaxAgeDays, 21)
}

// DiscussionMaxAge is the Discussion-candidate age window (default 14 days).
// Shorter than the Refreshed window on purpose: conversation on a three-week-old
// PR is usually about landing it, not about the review.
func (c Config) DiscussionMaxAge() time.Duration {
	return daysOr(c.Candidates.DiscussionMaxAgeDays, 14)
}

// RereviewCooldown is how long after one of our own real reviews a discovered
// candidate stays on hold (default 90m; an explicit "0s" disables the hold).
// Manual adds and promotion bypass it.
func (c Config) RereviewCooldown() time.Duration {
	return durationOrZero(c.Candidates.RereviewCooldown, 90*time.Minute)
}

// QuietPeriod is how long a PR must go untouched (no pushes, edits, or other
// updatedAt bumps) before discovery accepts it (default 15m; an explicit "0s"
// disables the hold). Guards against reviewing mid-rebase or mid-fix pushes.
func (c Config) QuietPeriod() time.Duration {
	return durationOrZero(c.Candidates.QuietPeriod, 15*time.Minute)
}

// SteeringHold is how long an open steering editor defers the PR being edited
// (default 5m; an explicit "0s" disables it), so the dispatcher cannot claim a
// row out from under somebody mid-sentence.
//
// Short on purpose, and renewed by the editor rather than set once: the hold
// has to expire on its own when a laptop sleeps or a tab closes, because
// nothing else will ever come along to release it.
func (c Config) SteeringHold() time.Duration {
	return durationOrZero(c.Candidates.SteeringHold, 5*time.Minute)
}

// ErrorBackoff is how long a PR waits after an engine attempt failed before
// it is tried again (default 15m; "0s" retires it on the first error, which
// was the old behaviour).
//
// An engine error is usually the world being briefly unavailable — a rate
// limit, a dropped connection, a killed subprocess — not a statement about
// the PR. Retiring the row on the first one dropped the PR until somebody
// pushed a commit, because discovery's same-SHA suppression keys on ANY
// recorded outcome.
func (c Config) ErrorBackoff() time.Duration {
	return durationOrZero(c.Candidates.ErrorBackoff, 15*time.Minute)
}

// SteeringHoldCap bounds how long renewal can keep a PR parked, however long
// the editor stays open. Without it a modal left open over lunch would defer a
// PR all afternoon: the expiry protects against a client that stops talking,
// and this protects against one that never stops.
func (c Config) SteeringHoldCap() time.Duration {
	return 4 * c.SteeringHold()
}

// MaxParallel is the concurrency cap: how many reviews run at once (default
// 4). Read per dispatch, so a raise takes effect without a restart.
func (c Config) MaxParallel() int {
	if c.Schedule.MaxParallel > 0 {
		return c.Schedule.MaxParallel
	}
	return 4
}

// Interval is the dispatcher's IDLE poll: how long it waits after a pull found
// nothing dispatchable (default 30s, and 30s on parse failure).
//
// It is no longer a batch cadence. A freed slot dispatches the next ready
// candidate after DispatchCooldown, without waiting for this interval, so the
// only thing this bounds is how long work the dispatcher cannot be told about
// sits unnoticed: a `queue add` from another process, or a hold expiring. That
// is why the default is seconds rather than the minute the batch cadence used:
// a manual add means "review this now", and nothing wakes the dispatcher for
// it. The cost is one ListQueue per interval while idle, each a duckdb
// subprocess holding the file lock for its ~25ms life, so a tighter default
// trades a little more lock contention for a shorter wait. Dial it down if the
// wait matters more to you than the churn.
//
// The key kept its name through the meaning change; LeaseWindow still derives
// from it, and its 2h floor absorbs any sane value here.
func (c Config) Interval() time.Duration {
	return durationOr(c.Schedule.Interval, 30*time.Second)
}

// DispatchCooldown is the pause between dispatches: after a slot frees, the
// dispatcher waits this long before claiming the next ready candidate
// (default 5s; an explicit "0s" disables the wait). It is a global gap
// between any two dispatches rather than a per-slot timer, because a single
// dispatcher owns every hand-off.
func (c Config) DispatchCooldown() time.Duration {
	return durationOrZero(c.Schedule.DispatchCooldown, 5*time.Second)
}

// ScheduleEnabled reports whether the review dispatcher runs (default true).
func (c Config) ScheduleEnabled() bool { return boolOr(c.Schedule.Enabled, true) }

// DiscoveryEnabled reports whether the discovery loop runs (default true).
func (c Config) DiscoveryEnabled() bool { return boolOr(c.Discovery.Enabled, true) }

func intOr(v *int, def int) int {
	if v == nil {
		return def
	}
	return *v
}

func boolOr(v *bool, def bool) bool {
	if v == nil {
		return def
	}
	return *v
}

// Bool returns a pointer to v for optional boolean config fields.
// Bool is a *bool literal helper for the optional enabled flags; production
// code reads them via the *Enabled getters, so its callers are tests.
func Bool(v bool) *bool { return &v }

// LeaseWindow is how long a claim stays authoritative before it is treated as
// abandoned by a crashed daemon. One definition serves the dispatcher's
// reclaim logic and the dashboard's "reviewing" badge; they must agree or the
// UI and the scheduler drift. The 2h floor keeps a short idle poll (say 15m)
// from shrinking the lease below a realistic review length: without it, a
// long review would look abandoned and get double-reviewed.
func (c Config) LeaseWindow() time.Duration {
	if w := c.Interval() * 4; w > 2*time.Hour {
		return w
	}
	return 2 * time.Hour
}

// TailscalePort is the Tailscale serve/funnel port (default 443).
func (c Config) TailscalePort() int {
	if c.Dashboard.Tailscale.Port != 0 {
		return c.Dashboard.Tailscale.Port
	}
	return 443
}

// DashboardAddr is the HTTP listen address (default "127.0.0.1:8330").
//
// Loopback, not ":8330". The dashboard has no auth of its own: reaching it is
// the authorisation, and `tailscale serve` is what grants that by proxying
// from the tailnet to this port. Binding every interface let anything that
// could reach the machine reach the dashboard directly, bypassing the proxy
// entirely, which matters twice over now that the proxy also attaches the
// identity headers the roster trusts: Tailscale strips forged copies on the
// way through, so a request that never goes through it can assert whatever it
// likes. `tailscale serve` proxies to 127.0.0.1 already, so this changes
// nothing for the supported setup. Bind wider only deliberately.
func (c Config) DashboardAddr() string {
	if c.Dashboard.Addr != "" {
		return c.Dashboard.Addr
	}
	return "127.0.0.1:8330"
}

// DiscoverInterval is the candidate-scraping cadence (default 5m; discovery
// is cheap gh calls, so it can run more often than reviews).
func (c Config) DiscoverInterval() time.Duration {
	return durationOr(c.Discovery.Interval, 5*time.Minute)
}

// RequireReviewRequest reports whether a PR must have an outstanding review
// request to be discovered (default false).
//
// The default is false because a PR that is open and not a draft is already
// saying it is ready; requiring somebody to also name a reviewer asks the
// author to do a second thing before this tool will look. On one watched repo
// that requirement hid 62 of the 100 most recently updated open PRs, which is
// not a queue of work nobody wanted reviewed.
//
// Set it true where a review request is meaningful: a repo whose team does
// assign reviewers is naming exactly the PRs that want attention, and that is
// a better signal than "open and not a draft" rather than a worse one.
//
// This is deliberately one bit, and the next question it raises is WHOSE
// request counts. Answering that needs a list of handles and teams to listen
// for, not a toggle; until it exists, true means "anyone asked".
func (c Config) RequireReviewRequest() bool {
	if c.Candidates.RequireReviewRequest != nil {
		return *c.Candidates.RequireReviewRequest
	}
	return false
}

// DiscoveryListLimit bounds one repo's PR listing (default 300). gh pages at
// 100 per request, so this is the per-repo page budget too: three requests,
// not the six a 500-PR repo would otherwise cost every cycle.
func (c Config) DiscoveryListLimit() int {
	if c.Discovery.ListLimit != nil && *c.Discovery.ListLimit > 0 {
		return *c.Discovery.ListLimit
	}
	return 300
}

// DiscoverySweepBudget caps one sweep's wall time. It defaults to the
// discovery interval so a sweep that pages slowly stops at its own next tick
// instead of overlapping it; the sweep resumes at the repo it did not reach,
// so a budget this tight starves nothing.
func (c Config) DiscoverySweepBudget() time.Duration {
	if d := durationOr(c.Discovery.SweepBudget, 0); d > 0 {
		return d
	}
	return c.DiscoverInterval()
}

// WatchesRepo reports whether repo is on the watch list (case-insensitive,
// matching GitHub's semantics). Discovery, the dashboard add gate, and the
// repos command all share this predicate.
func (c Config) WatchesRepo(repo string) bool {
	return RepoMatches(c.Repos, repo)
}

// AuthorScopedRepo reports membership of the legacy AllowedAuthorsOnlyRepos
// list (case-insensitive). Only unlistedGroup reads it now, as the fallback
// for a config that predates authors.unlisted; nothing else should, or the
// two ways of scoping a repo would disagree.
func (c Config) AuthorScopedRepo(repo string) bool {
	return RepoMatches(c.AllowedAuthorsOnlyRepos, repo)
}

// UsagePollInterval is the Codex usage refresh cadence (default 10m, and 10m
// on parse failure).
func (c Config) UsagePollInterval() time.Duration {
	return durationOr(c.Dashboard.UsagePollInterval, 10*time.Minute)
}

// StorePath is the DuckDB file location (default <XDG_DATA>/app.paulie.crew-code-review/queue.duckdb).
func (c Config) StorePath() string {
	if c.Store.Path != "" {
		return c.Store.Path
	}
	return filepath.Join(xdg.DataDir(appID), "queue.duckdb")
}

// PricingCacheDir is where the model price table is kept. Cache rather than
// data: it is re-fetchable, so losing it costs a download rather than a
// record, and it must never be backed up as if it were ours.
func PricingCacheDir() string {
	return xdg.CacheDir(appID)
}

// ReviewWorkspaceDir is where a review's scratch workspace, and so its agent
// transcript, is created.
//
// STATE, not the system temp dir it used to be. os.MkdirTemp("") put these
// under /var/folders on macOS, which the OS periodically sweeps: measured on a
// two-month-old install, 92% of the transcripts history still pointed at were
// already gone, so `queue log` and the dashboard's postmortem view were empty
// for almost everything. XDG calls this state precisely because it persists
// but is not precious: logs and history are its canonical examples. Cache
// would be wrong (nothing can re-fetch an agent transcript) and data would
// overstate it (losing one costs a postmortem, not a record).
func (c Config) ReviewWorkspaceDir() string {
	return filepath.Join(xdg.StateDir(appID), "reviews")
}

// WorkspaceRetention is how long a review's workspace is kept before the boot
// sweep removes it (default 30 days; "0s" keeps them forever).
//
// A retention policy is not optional here: nothing ever removed these, and the
// same install had 8,168 directories with no history row at all, from attempts
// that never completed. They only stopped growing because the OS was deleting
// them, which is exactly the behaviour being fixed.
func (c Config) WorkspaceRetention() time.Duration {
	return durationOrZero(c.Review.WorkspaceRetention, 30*24*time.Hour)
}

// durationOr parses s as a positive Go duration, else returns def: the one
// parse-or-default rule for every interval dial.
func durationOr(s string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return def
}

// durationOrZero is durationOr for dials where an explicit zero is meaningful
// ("0s" = disabled) rather than an unset value to default.
func durationOrZero(s string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(s); err == nil && d >= 0 {
		return d
	}
	return def
}

func daysOr(days, def int) time.Duration {
	if days <= 0 {
		days = def
	}
	return time.Duration(days) * 24 * time.Hour
}
