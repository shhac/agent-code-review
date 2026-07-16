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

// MaxParallel is the per-cycle concurrency cap (default 4).
func (c Config) MaxParallel() int {
	if c.Schedule.MaxParallel > 0 {
		return c.Schedule.MaxParallel
	}
	return 4
}

// Interval is the review cadence (default 1m, and 1m on parse failure). A
// tight default is safe: eligibility holds keep the queue empty of non-
// actionable work, and an idle cycle exits before recording anything.
func (c Config) Interval() time.Duration {
	return durationOr(c.Schedule.Interval, time.Minute)
}

// ScheduleEnabled reports whether the review loop runs (default true).
func (c Config) ScheduleEnabled() bool { return boolOr(c.Schedule.Enabled, true) }

// DiscoveryEnabled reports whether the discovery loop runs (default true).
func (c Config) DiscoveryEnabled() bool { return boolOr(c.Discovery.Enabled, true) }

// UsageFloor5h and UsageFloorWeekly are the remaining-percentage floors below
// which the review loop pauses (default 10; explicit 0 disables).
func (c Config) UsageFloor5h() int {
	return intOr(c.Schedule.UsageFloor.FiveHourPercent, 10)
}

func (c Config) UsageFloorWeekly() int {
	return intOr(c.Schedule.UsageFloor.WeeklyPercent, 10)
}

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
func Bool(v bool) *bool { return &v }

// LeaseWindow is how long a claim (or an unfinished run) stays authoritative
// before it is treated as abandoned by a crashed daemon. One definition
// serves the scheduler's reclaim logic, the run-lock staleness check, and
// the dashboard's "reviewing" badge; they must agree or the UI and the
// scheduler drift. The 2h floor keeps a short review interval (say 15m) from
// shrinking the lease below a realistic cycle length: without it, a long
// burst of reviews would look abandoned and get double-reviewed.
func (c Config) LeaseWindow() time.Duration {
	if w := c.Interval() * 4; w > 2*time.Hour {
		return w
	}
	return 2 * time.Hour
}

// Engine is the review engine id (default "codex").
func (c Config) Engine() string {
	if c.Review.Engine != "" {
		return c.Review.Engine
	}
	return "codex"
}

// DashboardAddr is the HTTP listen address (default ":8330").
func (c Config) DashboardAddr() string {
	if c.Dashboard.Addr != "" {
		return c.Dashboard.Addr
	}
	return ":8330"
}

// DiscoverInterval is the candidate-scraping cadence (default 10m; discovery
// is cheap gh calls, so it can run more often than reviews).
func (c Config) DiscoverInterval() time.Duration {
	return durationOr(c.Discovery.Interval, 10*time.Minute)
}

// WatchesRepo reports whether repo is on the watch list (case-insensitive,
// matching GitHub's semantics). Discovery, the dashboard add gate, and the
// repos command all share this predicate.
func (c Config) WatchesRepo(repo string) bool {
	return RepoMatches(c.Repos, repo)
}

// AuthorScopedRepo reports whether repo's discovery is limited to PRs from
// allowed authors (case-insensitive membership in AllowedAuthorsOnlyRepos).
func (c Config) AuthorScopedRepo(repo string) bool {
	return RepoMatches(c.AllowedAuthorsOnlyRepos, repo)
}

// UsagePollInterval is the Codex usage refresh cadence (default 10m, and 10m
// on parse failure).
func (c Config) UsagePollInterval() time.Duration {
	return durationOr(c.Dashboard.UsagePollInterval, 10*time.Minute)
}

// StorePath is the DuckDB file location (default <XDG_DATA>/agent-code-review/queue.duckdb).
func (c Config) StorePath() string {
	if c.Store.Path != "" {
		return c.Store.Path
	}
	return filepath.Join(xdg.DataDir(appName), "queue.duckdb")
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
