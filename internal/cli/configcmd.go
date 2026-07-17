package cli

import (
	"context"

	libcli "github.com/shhac/lib-agent-cli/cli"
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/review"
)

var (
	boolValues          = []string{"true", "false"}
	engineValues        = review.Engines
	sandboxValues       = []string{"read-only", "workspace-write", "danger-full-access"}
	tailscaleModeValues = []string{"serve", "funnel"}
)

// configKeySpec describes one editable scalar once: the lib-agent-cli key
// drives get/set/unset, while complete supplies its known values when there
// are any. This keeps validation and shell completion from evolving apart.
type configKeySpec struct {
	key      libcli.ConfigKey
	complete func(context.Context) []string
}

func registerConfig(root *cobra.Command) {
	specs := configKeySpecs()
	keys := configKeysFromSpecs(specs)
	cmd := libcli.ConfigCommand(globals, keys)
	attachConfigCompletions(cmd, specs)
	cmd.Short = "Get and set configuration (also: init, path, show)"
	cmd.AddCommand(
		&cobra.Command{
			Use:   "init",
			Short: "Write an annotated starter config (refuses to overwrite)",
			Args:  cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				path, err := config.Init()
				if err != nil {
					return err
				}
				return emit(map[string]string{"created": path, "next": "add repos via 'repos add', allow authors via 'authors allow', set prompts via 'prompts set'"})
			},
		},
		&cobra.Command{
			Use:   "path",
			Short: "Print the config file path",
			Args:  cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				return emit(map[string]string{"path": config.Path()})
			},
		},
		&cobra.Command{
			Use:   "show",
			Short: "Print the current resolved config",
			Args:  cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				return emit(config.Read())
			},
		},
	)
	registerGroupUsage(cmd, "config", configUsageText)
	root.AddCommand(cmd)
}

func configKeysFromSpecs(specs []configKeySpec) []libcli.ConfigKey {
	keys := make([]libcli.ConfigKey, 0, len(specs))
	for _, spec := range specs {
		keys = append(keys, spec.key)
	}
	return keys
}

func configKeySpecs() []configKeySpec {
	plain := func(key libcli.ConfigKey) configKeySpec { return configKeySpec{key: key} }
	static := func(key libcli.ConfigKey, values []string) configKeySpec {
		return configKeySpec{key: key, complete: func(context.Context) []string { return values }}
	}
	return []configKeySpec{
		plain(stringKey("gh_user", "GitHub login used for the self-review rule (empty = derive via `gh api user`)",
			func(c *config.Config) *string { return &c.GHUser }, nil)),
		static(optionalBoolKey("schedule.enabled", "Whether the serve daemon runs review cycles (default true)",
			func(c *config.Config) **bool { return &c.Schedule.Enabled }), boolValues),
		plain(stringKey("schedule.interval", "Review cadence as a Go duration (default 1m; idle cycles are no-ops)",
			func(c *config.Config) *string { return &c.Schedule.Interval }, validateDuration)),
		static(optionalBoolKey("discovery.enabled", "Whether the serve daemon scrapes repos for candidates (default true)",
			func(c *config.Config) **bool { return &c.Discovery.Enabled }), boolValues),
		plain(stringKey("discovery.interval", "Candidate-scraping cadence as a Go duration (default 10m; deterministic gh calls, no LLM)",
			func(c *config.Config) *string { return &c.Discovery.Interval }, validateDuration)),
		plain(intKey("schedule.max_parallel", "Max PRs reviewed concurrently per cycle (default 4)",
			func(c *config.Config) *int { return &c.Schedule.MaxParallel }, 1, 32)),
		plain(intKey("candidates.new_max_age_days", "Age window for New candidates (default 14)",
			func(c *config.Config) *int { return &c.Candidates.NewMaxAgeDays }, 1, 365)),
		plain(intKey("candidates.refreshed_max_age_days", "Age window for Refreshed candidates (default 21)",
			func(c *config.Config) *int { return &c.Candidates.RefreshedMaxAgeDays }, 1, 365)),
		plain(stringKey("candidates.rereview_cooldown", "Hold after one of our own reviews before re-discovery, as a Go duration (default 90m, 0s disables)",
			func(c *config.Config) *string { return &c.Candidates.RereviewCooldown }, validateHoldDuration)),
		plain(stringKey("candidates.quiet_period", "How long a PR must go untouched before discovery accepts it, as a Go duration (default 15m, 0s disables)",
			func(c *config.Config) *string { return &c.Candidates.QuietPeriod }, validateHoldDuration)),
		static(stringKey("review.engine", "Review engine (default codex)",
			func(c *config.Config) *string { return &c.Review.Engine }, validateOneOf("engine", engineValues)), engineValues),
		plain(stringKey("codex.bin", "Codex binary (default codex)",
			func(c *config.Config) *string { return &c.Review.Codex.Bin }, nil)),
		configKeySpec{key: stringKey("codex.model", "Model passed to codex exec --model",
			func(c *config.Config) *string { return &c.Review.Codex.Model }, nil), complete: codexModelSlugs},
		configKeySpec{key: stringKey("codex.effort", "Reasoning effort passed as Codex model_reasoning_effort (empty = model default)",
			func(c *config.Config) *string { return &c.Review.Codex.Effort }, nil), complete: completeConfiguredCodexEfforts},
		static(stringKey("codex.sandbox", "Codex sandbox mode (default workspace-write)",
			func(c *config.Config) *string { return &c.Review.Codex.Sandbox }, validateOneOf("sandbox mode", sandboxValues)), sandboxValues),
		plain(optionalIntKey("codex.max_resumes", "Resume nudges when a codex run ends on an intermediate WORKING report (default 2, 0 disables)",
			func(c *config.Config) **int { return &c.Review.Codex.MaxResumes }, 0, 10)),
		plain(stringKey("dashboard.addr", "Dashboard listen address (default :8330)",
			func(c *config.Config) *string { return &c.Dashboard.Addr }, nil)),
		static(stringKey("dashboard.tailscale.mode", `Tailscale exposure: "", "serve", or "funnel"`,
			func(c *config.Config) *string { return &c.Dashboard.Tailscale.Mode }, validateOneOf("tailscale mode", tailscaleModeValues)), tailscaleModeValues),
		plain(stringKey("dashboard.usage_poll_interval", "Codex usage refresh cadence as a Go duration (default 10m)",
			func(c *config.Config) *string { return &c.Dashboard.UsagePollInterval }, validateDuration)),
		plain(stringKey("store.path", "DuckDB file path (default under XDG data dir)",
			func(c *config.Config) *string { return &c.Store.Path }, nil)),
		plain(optionalIntKey("schedule.usage_floor.5h_percent", "Pause reviews when the 5h Codex window has less than this % remaining (default 10, 0 disables)",
			func(c *config.Config) **int { return &c.Schedule.UsageFloor.FiveHourPercent }, 0, 100)),
		plain(optionalIntKey("schedule.usage_floor.weekly_percent", "Pause reviews when the weekly Codex window has less than this % remaining (default 10, 0 disables)",
			func(c *config.Config) **int { return &c.Schedule.UsageFloor.WeeklyPercent }, 0, 100)),
	}
}
