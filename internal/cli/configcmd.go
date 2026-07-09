package cli

import (
	"strconv"
	"time"

	libcli "github.com/shhac/lib-agent-cli/cli"
	output "github.com/shhac/lib-agent-output"
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
)

func registerConfig(root *cobra.Command) {
	keys := configKeys()
	cmd := libcli.ConfigCommand(globals, keys)
	attachConfigCompletions(cmd, keys)
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

// configKeys defines the scalar dials editable via `config get|set|unset|list`.
// Repos, authors, and prompts have their own first-class command groups; rules
// and nested structures are edited in the file directly.
func configKeys() []libcli.ConfigKey {
	return []libcli.ConfigKey{
		stringKey("gh_user", "GitHub login used for the self-review rule (empty = derive via `gh api user`)",
			func(c *config.Config) *string { return &c.GHUser }, nil),
		optionalBoolKey("schedule.enabled", "Whether the serve daemon runs review cycles (default true)",
			func(c *config.Config) **bool { return &c.Schedule.Enabled }),
		stringKey("schedule.interval", "Review cadence as a Go duration (default 1m; idle cycles are no-ops)",
			func(c *config.Config) *string { return &c.Schedule.Interval }, validateDuration),
		optionalBoolKey("discovery.enabled", "Whether the serve daemon scrapes repos for candidates (default true)",
			func(c *config.Config) **bool { return &c.Discovery.Enabled }),
		stringKey("discovery.interval", "Candidate-scraping cadence as a Go duration (default 10m; deterministic gh calls, no LLM)",
			func(c *config.Config) *string { return &c.Discovery.Interval }, validateDuration),
		intKey("schedule.max_parallel", "Max PRs reviewed concurrently per cycle (default 4)",
			func(c *config.Config) *int { return &c.Schedule.MaxParallel }, 1, 32),
		intKey("candidates.new_max_age_days", "Age window for New candidates (default 14)",
			func(c *config.Config) *int { return &c.Candidates.NewMaxAgeDays }, 1, 365),
		intKey("candidates.refreshed_max_age_days", "Age window for Refreshed candidates (default 21)",
			func(c *config.Config) *int { return &c.Candidates.RefreshedMaxAgeDays }, 1, 365),
		stringKey("candidates.rereview_cooldown", "Hold after one of our own reviews before re-discovery, as a Go duration (default 90m, 0s disables)",
			func(c *config.Config) *string { return &c.Candidates.RereviewCooldown }, validateHoldDuration),
		stringKey("candidates.quiet_period", "How long a PR must go untouched before discovery accepts it, as a Go duration (default 15m, 0s disables)",
			func(c *config.Config) *string { return &c.Candidates.QuietPeriod }, validateHoldDuration),
		stringKey("review.engine", "Review engine (default codex)",
			func(c *config.Config) *string { return &c.Review.Engine }, validateEngine),
		stringKey("codex.bin", "Codex binary (default codex)",
			func(c *config.Config) *string { return &c.Review.Codex.Bin }, nil),
		stringKey("codex.model", "Model passed to codex exec --model",
			func(c *config.Config) *string { return &c.Review.Codex.Model }, nil),
		stringKey("codex.effort", "Reasoning effort passed as Codex model_reasoning_effort (empty = model default)",
			func(c *config.Config) *string { return &c.Review.Codex.Effort }, nil),
		stringKey("codex.sandbox", "Codex sandbox mode (default workspace-write)",
			func(c *config.Config) *string { return &c.Review.Codex.Sandbox }, validateSandbox),
		stringKey("dashboard.addr", "Dashboard listen address (default :8330)",
			func(c *config.Config) *string { return &c.Dashboard.Addr }, nil),
		stringKey("dashboard.tailscale.mode", `Tailscale exposure: "", "serve", or "funnel"`,
			func(c *config.Config) *string { return &c.Dashboard.Tailscale.Mode }, validateTailscaleMode),
		stringKey("dashboard.usage_poll_interval", "Codex usage refresh cadence as a Go duration (default 10m)",
			func(c *config.Config) *string { return &c.Dashboard.UsagePollInterval }, validateDuration),
		stringKey("store.path", "DuckDB file path (default under XDG data dir)",
			func(c *config.Config) *string { return &c.Store.Path }, nil),
		optionalIntKey("schedule.usage_floor.5h_percent", "Pause reviews when the 5h Codex window has less than this % remaining (default 10, 0 disables)",
			func(c *config.Config) **int { return &c.Schedule.UsageFloor.FiveHourPercent }, 0, 100),
		optionalIntKey("schedule.usage_floor.weekly_percent", "Pause reviews when the weekly Codex window has less than this % remaining (default 10, 0 disables)",
			func(c *config.Config) **int { return &c.Schedule.UsageFloor.WeeklyPercent }, 0, 100),
	}
}

// stringKey builds a ConfigKey over a string field; validate may be nil.
func stringKey(name, desc string, field func(*config.Config) *string, validate func(string) error) libcli.ConfigKey {
	return libcli.ConfigKey{
		Name:        name,
		Description: desc,
		Get: func() (string, bool) {
			cfg := config.Read()
			v := *field(&cfg)
			return v, v != ""
		},
		Set: func(value string) error {
			if validate != nil {
				if err := validate(value); err != nil {
					return err
				}
			}
			return config.Update(func(cfg *config.Config) error {
				*field(cfg) = value
				return nil
			})
		},
		Unset: func() error {
			return config.Update(func(cfg *config.Config) error {
				*field(cfg) = ""
				return nil
			})
		},
	}
}

func optionalBoolKey(name, desc string, field func(*config.Config) **bool) libcli.ConfigKey {
	return libcli.ConfigKey{
		Name:        name,
		Description: desc,
		Get: func() (string, bool) {
			cfg := config.Read()
			p := *field(&cfg)
			if p == nil {
				return "", false
			}
			return strconv.FormatBool(*p), true
		},
		Set: func(value string) error {
			b, err := strconv.ParseBool(value)
			if err != nil {
				return output.New("Value must be true or false, got "+value, output.FixableByAgent)
			}
			return config.Update(func(cfg *config.Config) error {
				*field(cfg) = &b
				return nil
			})
		},
		Unset: func() error {
			return config.Update(func(cfg *config.Config) error {
				*field(cfg) = nil
				return nil
			})
		},
	}
}

func intKey(name, desc string, field func(*config.Config) *int, min, max int) libcli.ConfigKey {
	return libcli.ConfigKey{
		Name:        name,
		Description: desc,
		Get: func() (string, bool) {
			cfg := config.Read()
			v := *field(&cfg)
			if v == 0 {
				return "", false
			}
			return strconv.Itoa(v), true
		},
		Set: func(value string) error {
			n, err := parseBoundedInt(value, min, max)
			if err != nil {
				return err
			}
			return config.Update(func(cfg *config.Config) error {
				*field(cfg) = n
				return nil
			})
		},
		Unset: func() error {
			return config.Update(func(cfg *config.Config) error {
				*field(cfg) = 0
				return nil
			})
		},
	}
}

// optionalIntKey is intKey over a nullable field: unset restores the coded
// default (nil), and an explicit value (including 0) is stored as set.
func optionalIntKey(name, desc string, field func(*config.Config) **int, min, max int) libcli.ConfigKey {
	return libcli.ConfigKey{
		Name:        name,
		Description: desc,
		Get: func() (string, bool) {
			cfg := config.Read()
			p := *field(&cfg)
			if p == nil {
				return "", false
			}
			return strconv.Itoa(*p), true
		},
		Set: func(value string) error {
			n, err := parseBoundedInt(value, min, max)
			if err != nil {
				return err
			}
			return config.Update(func(cfg *config.Config) error {
				*field(cfg) = &n
				return nil
			})
		},
		Unset: func() error {
			return config.Update(func(cfg *config.Config) error {
				*field(cfg) = nil
				return nil
			})
		},
	}
}

// parseBoundedInt is the shared validation for integer config keys — one
// source for the bounds check and its error wording.
func parseBoundedInt(value string, min, max int) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n < min || n > max {
		return 0, output.Newf(output.FixableByAgent, "Value must be an integer in [%d, %d], got %s", min, max, value)
	}
	return n, nil
}

func validateDuration(v string) error {
	if v == "" {
		return nil
	}
	if d, err := time.ParseDuration(v); err != nil || d <= 0 {
		return output.New("Value must be a positive Go duration (e.g. 30m, 1h), got "+v, output.FixableByAgent)
	}
	return nil
}

// validateHoldDuration is validateDuration for the eligibility-hold dials,
// where an explicit zero ("0s") is meaningful: it disables the hold.
func validateHoldDuration(v string) error {
	if v == "" {
		return nil
	}
	if d, err := time.ParseDuration(v); err != nil || d < 0 {
		return output.New("Value must be a non-negative Go duration (e.g. 90m, 0s to disable), got "+v, output.FixableByAgent)
	}
	return nil
}

func validateEngine(v string) error {
	if v == "" || v == "codex" {
		return nil
	}
	return output.New(`Unknown engine: `+v+`. Valid: codex`, output.FixableByAgent)
}

func validateSandbox(v string) error {
	switch v {
	case "", "read-only", "workspace-write", "danger-full-access":
		return nil
	}
	return output.New("Invalid sandbox mode: "+v+". Valid: read-only, workspace-write, danger-full-access", output.FixableByAgent)
}

func validateTailscaleMode(v string) error {
	switch v {
	case "", "serve", "funnel":
		return nil
	}
	return output.New(`Invalid tailscale mode: `+v+`. Valid: "", serve, funnel`, output.FixableByAgent)
}
