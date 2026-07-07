// Package config owns ~/.config/agent-code-review/config.json: the repos to
// watch, the approval allow-list, candidate age thresholds, schedule cadence,
// the review engine + prompt/rules, the DuckDB store location, and the
// dashboard/Tailscale settings. Everything the CLI treats as tunable lives
// here — no GitHub handles, repos, or prompts are hardcoded in code.
package config

import (
	"path/filepath"
	"time"

	"github.com/shhac/lib-agent-cli/creds"
	"github.com/shhac/lib-agent-cli/xdg"
)

const appName = "agent-code-review"

// Condition gates a Rule. An empty field means "don't care"; all set fields
// must match for the rule's prompt fragment to be appended. These map onto
// the deterministic facts the CLI knows about a candidate before invoking the
// engine.
type Condition struct {
	AuthorIsGHUser       bool     `json:"author_is_gh_user,omitempty"`
	AuthorNotInAllowlist bool     `json:"author_not_in_allowlist,omitempty"`
	CandidateType        string   `json:"candidate_type,omitempty"` // "new" | "refreshed" | ""
	Repos                []string `json:"repos,omitempty"`          // "owner/name" match, any-of
}

// Rule is a conditional prompt fragment: "when <condition>, add <prompt> to
// the engine's instructions". This is how self-review and non-allow-list
// authors get downgraded to comment-only — via prompt, not Go code.
type Rule struct {
	Name   string    `json:"name"`
	When   Condition `json:"when"`
	Prompt string    `json:"prompt"`
}

// CandidateSettings holds the age windows from the schedule spec.
type CandidateSettings struct {
	NewMaxAgeDays       int `json:"new_max_age_days,omitempty"`       // default 14
	RefreshedMaxAgeDays int `json:"refreshed_max_age_days,omitempty"` // default 21
}

// ScheduleSettings drives the serve daemon's internal ticker.
type ScheduleSettings struct {
	Enabled     bool   `json:"enabled,omitempty"`
	Interval    string `json:"interval,omitempty"`     // Go duration, e.g. "30m"
	MaxParallel int    `json:"max_parallel,omitempty"` // default 4
}

// CodexSettings configures the default review engine (codex exec).
type CodexSettings struct {
	Bin     string   `json:"bin,omitempty"`     // default "codex"
	Model   string   `json:"model,omitempty"`   // e.g. "gpt-5.5-codex"
	Sandbox string   `json:"sandbox,omitempty"` // codex sandbox mode
	Args    []string `json:"args,omitempty"`    // extra args appended to `codex exec`
}

// ReviewSettings selects and configures the pluggable review engine.
type ReviewSettings struct {
	Engine         string        `json:"engine,omitempty"`           // "codex" (default) | "claude" (later)
	MainPrompt     string        `json:"main_prompt,omitempty"`      // inline main review prompt
	MainPromptPath string        `json:"main_prompt_path,omitempty"` // or load it from a file
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
}

// Config is the whole on-disk document.
type Config struct {
	Repos  []string `json:"repos,omitempty"`
	GHUser string   `json:"gh_user,omitempty"` // optional; else derived via `gh api user`
	// The approver allow-list lives in the store (per repo), not here — manage
	// it with `agent-code-review approvers`.
	Candidates CandidateSettings `json:"candidates,omitempty"`
	Schedule   ScheduleSettings  `json:"schedule,omitempty"`
	Review     ReviewSettings    `json:"review,omitempty"`
	Store      StoreSettings     `json:"store,omitempty"`
	Dashboard  DashboardSettings `json:"dashboard,omitempty"`
}

// Dir is ~/.config/agent-code-review (respects XDG_CONFIG_HOME).
func Dir() string { return xdg.ConfigDir(appName) }

func filePath() string { return filepath.Join(Dir(), "config.json") }

func store() creds.Store { return creds.Store{Path: filePath()} }

// Read returns the parsed config, or a zero Config when the file is missing or
// unparseable — a corrupt file behaves like an empty one rather than wedging
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

// Path exposes the config file location for the `config path` command.
func Path() string { return filePath() }

// --- resolved getters: apply defaults so callers never special-case zero ---

// NewMaxAge is the New-candidate age window (default 14 days).
func (c Config) NewMaxAge() time.Duration {
	return daysOr(c.Candidates.NewMaxAgeDays, 14)
}

// RefreshedMaxAge is the Refreshed-candidate age window (default 21 days).
func (c Config) RefreshedMaxAge() time.Duration {
	return daysOr(c.Candidates.RefreshedMaxAgeDays, 21)
}

// MaxParallel is the per-cycle concurrency cap (default 4).
func (c Config) MaxParallel() int {
	if c.Schedule.MaxParallel > 0 {
		return c.Schedule.MaxParallel
	}
	return 4
}

// Interval is the scheduler cadence (default 30m, and 30m on parse failure).
func (c Config) Interval() time.Duration {
	if c.Schedule.Interval == "" {
		return 30 * time.Minute
	}
	if d, err := time.ParseDuration(c.Schedule.Interval); err == nil && d > 0 {
		return d
	}
	return 30 * time.Minute
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

// StorePath is the DuckDB file location (default <XDG_DATA>/agent-code-review/queue.duckdb).
func (c Config) StorePath() string {
	if c.Store.Path != "" {
		return c.Store.Path
	}
	return filepath.Join(xdg.DataDir(appName), "queue.duckdb")
}

func daysOr(days, def int) time.Duration {
	if days <= 0 {
		days = def
	}
	return time.Duration(days) * 24 * time.Hour
}
