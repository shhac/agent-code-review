// Package config owns ~/.config/agent-code-review/config.json: the repos to
// watch, the approval allow-list, candidate age thresholds, schedule cadence,
// the review engine + prompt/rules, the DuckDB store location, and the
// dashboard/Tailscale settings. Everything the CLI treats as tunable lives
// here; no GitHub handles, repos, or prompts are hardcoded in code.
package config

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/shhac/lib-agent-cli/creds"
	"github.com/shhac/lib-agent-cli/xdg"
)

const appName = "agent-code-review"

// repoNamePattern is the one definition of the accepted "owner/name" shape;
// the CLI and dashboard validators both consume it via ValidRepoName.
var repoNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// ValidRepoName reports whether s looks like an "owner/name" repo reference.
func ValidRepoName(s string) bool { return repoNamePattern.MatchString(s) }

// Outcomes are the post-outcome bullets a rule fragment can be routed under.
// They mirror the review outcomes the agent can land on (reject = requested
// changes). SKIPPED has no prompt slot, so it is not routable.
var Outcomes = []string{"approve", "comment", "reject"}

// ValidOutcome reports whether s names a routable post-outcome bullet.
func ValidOutcome(s string) bool {
	for _, o := range Outcomes {
		if s == o {
			return true
		}
	}
	return false
}

// CandidateTypes are the discovery kinds a rule can gate on.
var CandidateTypes = []string{"new", "refreshed"}

// ValidCandidateType reports whether s names a candidate discovery kind.
func ValidCandidateType(s string) bool {
	for _, t := range CandidateTypes {
		if s == t {
			return true
		}
	}
	return false
}

// starterJSON is the annotated starter config written by `config init`. It is
// the same content as the repo's config.example.json (a test keeps them in
// lockstep).
//
//go:embed starter.json
var starterJSON []byte

// Condition gates a Rule. An empty field means "don't care"; all set fields
// must match for the rule's prompt fragment to be appended. These map onto
// the deterministic facts the CLI knows about a candidate before invoking the
// engine.
//
// Outcome is not a fact: it routes the fragment under a specific post-outcome
// bullet (approve/comment/reject) instead of the prompt body. It is matched by
// the outcome the agent lands on, never gated against candidate facts.
type Condition struct {
	AuthorIsGHUser   bool     `json:"author_is_gh_user,omitempty"`  // author IS our gh user (self-authored)
	AuthorNotGHUser  bool     `json:"author_not_gh_user,omitempty"` // author is NOT our gh user (not self-authored)
	AuthorAllowed    bool     `json:"author_allowed,omitempty"`     // author IS on the allowed-authors list for this repo
	AuthorNotAllowed bool     `json:"author_not_allowed,omitempty"` // author not on the allowed-authors list for this repo
	CandidateType    string   `json:"candidate_type,omitempty"`     // "new" | "refreshed" | ""
	Repos            []string `json:"repos,omitempty"`              // "owner/name" match, any-of
	Outcome          string   `json:"outcome,omitempty"`            // "approve" | "comment" | "reject" | "": route under this outcome's bullet
}

// Rule is a conditional prompt fragment: "when <condition>, add <prompt> to
// the engine's instructions". This is how self-review and non-allow-list
// authors get downgraded to comment-only, via prompt, not Go code.
type Rule struct {
	Name   string    `json:"name"`
	When   Condition `json:"when"`
	Prompt string    `json:"prompt"`
}

// CandidateSettings holds the age windows from the schedule spec plus the two
// eligibility holds: how long after our own review a PR stays on hold
// (rereview_cooldown) and how long a PR must sit untouched before we accept it
// (quiet_period). Holds defer discovered candidates; manual adds bypass both.
type CandidateSettings struct {
	NewMaxAgeDays       int    `json:"new_max_age_days,omitempty"`       // default 14
	RefreshedMaxAgeDays int    `json:"refreshed_max_age_days,omitempty"` // default 21
	RereviewCooldown    string `json:"rereview_cooldown,omitempty"`      // Go duration, default "90m"; "0s" disables
	QuietPeriod         string `json:"quiet_period,omitempty"`           // Go duration, default "15m"; "0s" disables
}

// ScheduleSettings drives the review loop: LLM invocations, so it carries the
// parallelism cap. Discovery has its own independent settings (DiscoverySettings).
type ScheduleSettings struct {
	Enabled     *bool            `json:"enabled,omitempty"`
	Interval    string           `json:"interval,omitempty"`     // review cadence, e.g. "30m"
	MaxParallel int              `json:"max_parallel,omitempty"` // default 4
	UsageFloor  UsageFloorLimits `json:"usage_floor,omitempty"`
}

// UsageFloorLimits pauses the review loop while Codex usage headroom is low:
// when a window's remaining percentage drops below its floor, no new review
// cycle starts until the window refills. nil means the default (10); an
// explicit 0 disables that window's floor.
type UsageFloorLimits struct {
	FiveHourPercent *int `json:"5h_percent,omitempty"`
	WeeklyPercent   *int `json:"weekly_percent,omitempty"`
}

// DiscoverySettings drives the candidate-scraping loop: cheap, deterministic
// gh calls (no LLM, hence no parallelism dial) with its own on/off switch so
// scraping can run without reviews (or vice versa).
type DiscoverySettings struct {
	Enabled  *bool  `json:"enabled,omitempty"`
	Interval string `json:"interval,omitempty"` // e.g. "10m"
}

// CodexSettings configures the default review engine (codex exec).
type CodexSettings struct {
	Bin     string   `json:"bin,omitempty"`     // default "codex"
	Model   string   `json:"model,omitempty"`   // e.g. "gpt-5.6"
	Effort  string   `json:"effort,omitempty"`  // Codex model_reasoning_effort; empty = model default
	Sandbox string   `json:"sandbox,omitempty"` // codex sandbox mode
	Args    []string `json:"args,omitempty"`    // extra args appended to `codex exec`
}

// ReviewSettings selects and configures the pluggable review engine.
//
// OnApprove/OnComment/OnReject are post-outcome prompt fragments: instructions
// the agent follows after landing on that outcome (approve / comment without
// approving / request changes). Workspace-specific knowledge (Slack channels,
// emoji conventions, extra CLIs) belongs HERE, in the user's config, never in
// the tool or its shipped defaults. The tool itself assumes only gh and codex.
type ReviewSettings struct {
	Engine         string        `json:"engine,omitempty"`           // "codex" (default) | "claude" (later)
	MainPrompt     string        `json:"main_prompt,omitempty"`      // inline main review prompt
	MainPromptPath string        `json:"main_prompt_path,omitempty"` // or load it from a file
	OnApprove      string        `json:"on_approve,omitempty"`
	OnComment      string        `json:"on_comment,omitempty"`
	OnReject       string        `json:"on_reject,omitempty"` // reject = requested changes
	Rules          []Rule        `json:"rules,omitempty"`
	Codex          CodexSettings `json:"codex,omitempty"`
}

// StoreSettings locates the persistent DuckDB file.
type StoreSettings struct {
	Engine string `json:"engine,omitempty"` // "duckdb" (default)
	Path   string `json:"path,omitempty"`   // default: <XDG_DATA>/agent-code-review/queue.duckdb
}

// TailscaleSettings mirrors lib-agent-mcp/tailscale: mode "" (off), "serve"
// (tailnet-private) or "funnel" (public), on port 443/8443/10000.
type TailscaleSettings struct {
	Mode string `json:"mode,omitempty"`
	Port int    `json:"port,omitempty"`
}

// DashboardSettings configures the web UI served by `serve`.
type DashboardSettings struct {
	Addr      string            `json:"addr,omitempty"`       // default ":8330"
	PublicURL string            `json:"public_url,omitempty"` // derived from Tailscale when unset
	Tailscale TailscaleSettings `json:"tailscale,omitempty"`
	// UsagePollInterval is how often the daemon refreshes Codex usage for the
	// dashboard (Go duration, default 10m).
	UsagePollInterval string `json:"usage_poll_interval,omitempty"`
}

// Config is the whole on-disk document.
type Config struct {
	Repos []string `json:"repos,omitempty"`
	// AllowedAuthorsOnlyRepos scopes discovery for the listed repos to PRs
	// authored by allowed authors. Repos not listed discover any open PR (the
	// default); the allowed-authors list then only governs approve vs
	// comment-only. Use for repos where reviewing every PR would be noise.
	AllowedAuthorsOnlyRepos []string `json:"allowed_authors_only_repos,omitempty"`
	GHUser                  string   `json:"gh_user,omitempty"` // optional; else derived via `gh api user`
	// The allowed-authors list (whose PRs we may approve) lives in the store,
	// per repo, not here; manage it with `agent-code-review authors`.
	Candidates CandidateSettings `json:"candidates,omitempty"`
	Schedule   ScheduleSettings  `json:"schedule,omitempty"`
	Discovery  DiscoverySettings `json:"discovery,omitempty"`
	Review     ReviewSettings    `json:"review,omitempty"`
	Store      StoreSettings     `json:"store,omitempty"`
	Dashboard  DashboardSettings `json:"dashboard,omitempty"`
}

// Dir is ~/.config/agent-code-review (respects XDG_CONFIG_HOME).
func Dir() string { return xdg.ConfigDir(appName) }

func filePath() string { return filepath.Join(Dir(), "config.json") }

func store() creds.Store { return creds.Store{Path: filePath()} }

// Read returns the parsed config, or a zero Config when the file is missing or
// unparseable; a corrupt file behaves like an empty one rather than wedging
// every command.
func Read() Config {
	var cfg Config
	if err := store().Load(&cfg); err != nil {
		return Config{}
	}
	return cfg
}

// Write persists the config (0600 file, 0700 dirs, via creds.Store).
func Write(cfg Config) error { return store().Save(cfg) }

// Update applies mutate to one current config snapshot, then persists it. It
// keeps every config command on the same read-once/write-once transaction
// shape without implying cross-process locking.
func Update(mutate func(*Config) error) error {
	cfg := Read()
	if err := mutate(&cfg); err != nil {
		return err
	}
	return Write(cfg)
}

// Path exposes the config file location for the `config path` command.
func Path() string { return filePath() }

// Init writes the annotated starter config, refusing to overwrite an existing
// file; `config init` must never clobber a live setup.
func Init() (string, error) {
	path := filePath()
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("Config already exists at %s: edit it directly, or remove it first", path)
	}
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, starterJSON, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// StarterJSON exposes the embedded starter for the lockstep test.
func StarterJSON() []byte { return starterJSON }

// --- resolved getters: apply defaults so callers never special-case zero ---

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

// RepoMatches reports whether want is in list using GitHub repo identity
// semantics (case-insensitive owner/name match).
func RepoMatches(list []string, want string) bool {
	for _, r := range list {
		if strings.EqualFold(r, want) {
			return true
		}
	}
	return false
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
