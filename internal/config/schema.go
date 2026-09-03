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
	AuthorIsGHUser  bool `json:"author_is_gh_user,omitempty"`  // author IS our gh user (self-authored)
	AuthorNotGHUser bool `json:"author_not_gh_user,omitempty"` // author is NOT our gh user (not self-authored)
	// AuthorAllowed and AuthorNotAllowed predate groups, where an author was
	// one bit. They survive as aliases for "the resolved policy does (not)
	// permit approval", so rules written before groups keep their meaning.
	AuthorAllowed    bool     `json:"author_allowed,omitempty"`
	AuthorNotAllowed bool     `json:"author_not_allowed,omitempty"`
	Groups           []string `json:"groups,omitempty"`         // author's resolved group, any-of
	Authors          []string `json:"authors,omitempty"`        // author handle, any-of (case-insensitive)
	CandidateType    string   `json:"candidate_type,omitempty"` // "new" | "refreshed" | ""
	Repos            []string `json:"repos,omitempty"`          // "owner/name" match, any-of
	Outcome          string   `json:"outcome,omitempty"`        // "approve" | "comment" | "reject" | "": route under this outcome's section
}

// Rule is a conditional prompt fragment: "when <condition>, add <prompt> to
// the engine's instructions". This is how self-review and non-allow-list
// authors get downgraded to comment-only, via prompt, not Go code.
type Rule struct {
	Name   string    `json:"name"`
	When   Condition `json:"when"`
	Prompt string    `json:"prompt"`
}

// Group is one author cohort's complete review policy: what we may do with
// their PRs, which engine does it, and what extra instruction the agent gets.
// Every field is optional; an empty field inherits from the layer beneath it
// (see Config.ResolvePolicy). Group definitions live here, in config, next to
// the prompts and engine dials they carry; which authors are IN a group is
// roster data and lives in the store.
type Group struct {
	Review string `json:"review,omitempty"` // "ignore" | "comment" | "approve"; empty = comment
	Engine string `json:"engine,omitempty"` // overrides review.engine for this cohort
	Model  string `json:"model,omitempty"`  // overrides the resolved engine's model
	Effort string `json:"effort,omitempty"` // overrides the resolved engine's reasoning effort
	Prompt string `json:"prompt,omitempty"` // appended to the review instructions
}

// AuthorOverride narrows a policy below the group: the same patchable fields,
// applied to one handle, optionally on one set of repos. It is literally a
// group patch, hence the embedding, so a field means the same thing in both
// places and a new dial is added once.
type AuthorOverride struct {
	Handle string   `json:"handle"`
	Repos  []string `json:"repos,omitempty"` // "owner/name" or "*"; empty = every repo
	Group
}

// AuthorSettings is the group system: the cohort definitions, where authors
// with no membership row land, and the per-handle narrowings on top.
type AuthorSettings struct {
	// Unlisted maps a repo ("owner/name", or "*" for the fallback) to the
	// group an author with no membership row resolves to. It supersedes
	// AllowedAuthorsOnlyRepos, which is still honored for configs that have
	// not adopted this (see Config.unlistedGroup).
	Unlisted map[string]string `json:"unlisted,omitempty"`
	// Groups are the cohort definitions by name. The built-in names
	// (approver / commenter / ignored) exist without being declared; defining
	// one here of the same name replaces it.
	Groups    map[string]Group `json:"groups,omitempty"`
	Overrides []AuthorOverride `json:"overrides,omitempty"`
}

// CandidateSettings holds the age windows from the schedule spec plus the two
// eligibility holds: how long after our own review a PR stays on hold
// (rereview_cooldown) and how long a PR must sit untouched before we accept it
// (quiet_period). Holds defer discovered candidates; manual adds bypass both.
type CandidateSettings struct {
	NewMaxAgeDays        int    `json:"new_max_age_days,omitempty"`        // default 14
	RefreshedMaxAgeDays  int    `json:"refreshed_max_age_days,omitempty"`  // default 21
	DiscussionMaxAgeDays int    `json:"discussion_max_age_days,omitempty"` // default 14
	RereviewCooldown     string `json:"rereview_cooldown,omitempty"`       // Go duration, default "90m"; "0s" disables
	QuietPeriod          string `json:"quiet_period,omitempty"`            // Go duration, default "15m"; "0s" disables
}

// ScheduleSettings drives the review dispatcher: LLM invocations, so it
// carries the parallelism cap. Discovery has its own independent settings
// (DiscoverySettings).
type ScheduleSettings struct {
	Enabled *bool `json:"enabled,omitempty"`
	// Interval is the dispatcher's IDLE poll: how long it waits before
	// looking again after a pull found nothing dispatchable. It is no longer
	// a batch cadence: a freed slot dispatches the next candidate without
	// waiting for it.
	Interval         string           `json:"interval,omitempty"`          // idle poll, e.g. "30m"
	MaxParallel      int              `json:"max_parallel,omitempty"`      // default 4
	DispatchCooldown string           `json:"dispatch_cooldown,omitempty"` // Go duration, default "5s"; "0s" disables
	UsageFloor       UsageFloorLimits `json:"usage_floor,omitempty"`
}

// UsageFloorLimits holds a candidate back while its engine's usage headroom
// is low: when a window's remaining percentage drops below its floor, that
// engine's candidates are not dispatched until the window refills. nil means
// the default (10); an explicit 0 disables that window's floor.
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
	Bin        string   `json:"bin,omitempty"`         // default "codex"
	Model      string   `json:"model,omitempty"`       // e.g. "gpt-5.6"
	Effort     string   `json:"effort,omitempty"`      // Codex model_reasoning_effort; empty = model default
	Sandbox    string   `json:"sandbox,omitempty"`     // codex sandbox mode
	Args       []string `json:"args,omitempty"`        // extra args appended to `codex exec`
	MaxResumes *int     `json:"max_resumes,omitempty"` // resumes when a run ends on a WORKING report; nil = default 2, 0 disables
}

// ClaudeSettings configures the claude review engine (`claude -p`).
//
// PermissionMode is the analogue of codex's Sandbox: it decides what the agent
// may do without a prompt it could never answer headlessly. AllowedTools
// narrows that further per tool. MaxBudgetUSD is a hard per-invocation ceiling
// with no codex equivalent; on a subscription the figure is the notional
// API-rate valuation of the run, so it bounds runaway reviews rather than
// literal spend.
type ClaudeSettings struct {
	Bin            string   `json:"bin,omitempty"`             // default "claude"
	Model          string   `json:"model,omitempty"`           // alias ("opus", "sonnet") or full id
	Effort         string   `json:"effort,omitempty"`          // low|medium|high|xhigh|max; empty = session default
	PermissionMode string   `json:"permission_mode,omitempty"` // default "acceptEdits"
	AllowedTools   []string `json:"allowed_tools,omitempty"`   // extra pre-approved tools
	MaxBudgetUSD   float64  `json:"max_budget_usd,omitempty"`  // --max-budget-usd; 0 = uncapped
	Args           []string `json:"args,omitempty"`            // extra args appended to `claude -p`
	MaxResumes     *int     `json:"max_resumes,omitempty"`     // resumes when a run ends without a final report; nil = default 2, 0 disables
}

// ReviewSettings selects and configures the pluggable review engine.
//
// OnApprove/OnComment/OnReject are post-outcome prompt fragments: instructions
// the agent follows after landing on that outcome (approve / comment without
// approving / request changes). Workspace-specific knowledge (Slack channels,
// emoji conventions, extra CLIs) belongs HERE, in the user's config, never in
// the tool or its shipped defaults. The tool itself assumes only gh and codex.
type ReviewSettings struct {
	Engine         string         `json:"engine,omitempty"`           // "codex" (default) | "claude"
	MainPrompt     string         `json:"main_prompt,omitempty"`      // inline main review prompt
	MainPromptPath string         `json:"main_prompt_path,omitempty"` // or load it from a file
	OnApprove      string         `json:"on_approve,omitempty"`
	OnComment      string         `json:"on_comment,omitempty"`
	OnReject       string         `json:"on_reject,omitempty"`     // reject = requested changes
	ResumePrompt   string         `json:"resume_prompt,omitempty"` // nudge when resuming a run that ended on WORKING; empty = built-in default
	Rules          []Rule         `json:"rules,omitempty"`
	Codex          CodexSettings  `json:"codex,omitempty"`
	Claude         ClaudeSettings `json:"claude,omitempty"`
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
	// AllowedAuthorsOnlyRepos is the pre-groups way to scope discovery: listed
	// repos only discovered PRs from rostered authors. Superseded by
	// Authors.Unlisted, which says the same thing and more, but still honored
	// so a config that has not adopted groups keeps its exact behaviour (see
	// Config.unlistedGroup). New configs should use authors.unlisted.
	AllowedAuthorsOnlyRepos []string `json:"allowed_authors_only_repos,omitempty"`
	GHUser                  string   `json:"gh_user,omitempty"` // optional; else derived via `gh api user`
	// Authors is the group system: cohort definitions, the unlisted fallback,
	// and per-handle overrides. Which authors are IN each group lives in the
	// store, per repo; manage it with `agent-code-review authors`.
	Authors    AuthorSettings    `json:"authors,omitempty"`
	Candidates CandidateSettings `json:"candidates,omitempty"`
	Schedule   ScheduleSettings  `json:"schedule,omitempty"`
	Discovery  DiscoverySettings `json:"discovery,omitempty"`
	Review     ReviewSettings    `json:"review,omitempty"`
	Store      StoreSettings     `json:"store,omitempty"`
	Dashboard  DashboardSettings `json:"dashboard,omitempty"`
}
