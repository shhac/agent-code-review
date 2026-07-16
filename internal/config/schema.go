package config

// This file is the on-disk config.json schema: every struct that maps to a
// piece of the document. Persistence lives in config.go, resolved defaults in
// defaults.go, and value validation in validate.go.

// Condition gates a Rule. An empty field means "don't care"; all set fields
// must match for the rule's prompt fragment to be appended. These map onto
// the deterministic facts the CLI knows about a candidate before invoking the
// engine.
//
// Outcome is not a fact: it routes the fragment under a specific post-outcome
// section (approve/comment/reject) instead of the prompt body. It is matched by
// the outcome the agent lands on, never gated against candidate facts.
type Condition struct {
	AuthorIsGHUser   bool     `json:"author_is_gh_user,omitempty"`  // author IS our gh user (self-authored)
	AuthorNotGHUser  bool     `json:"author_not_gh_user,omitempty"` // author is NOT our gh user (not self-authored)
	AuthorAllowed    bool     `json:"author_allowed,omitempty"`     // author IS on the allowed-authors list for this repo
	AuthorNotAllowed bool     `json:"author_not_allowed,omitempty"` // author not on the allowed-authors list for this repo
	CandidateType    string   `json:"candidate_type,omitempty"`     // "new" | "refreshed" | ""
	Repos            []string `json:"repos,omitempty"`              // "owner/name" match, any-of
	Outcome          string   `json:"outcome,omitempty"`            // "approve" | "comment" | "reject" | "": route under this outcome's section
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
