package cli

import (
	"slices"
	"strconv"
	"time"

	libcli "github.com/shhac/lib-agent-cli/cli"
	output "github.com/shhac/lib-agent-output"

	"github.com/shhac/agent-code-review/internal/config"
)

// configKeyOf is the one Get/Set/Unset scaffold behind every key kind: parse
// turns user input into the stored value (carrying validation), format
// renders the stored value (false = unset), and unset is the stored zero.
// The named builders below are thin parameterizations; the key catalog lives
// in configcmd.go.
func configKeyOf[T any](name, desc string, field func(*config.Config) *T, parse func(string) (T, error), format func(T) (string, bool), unset T) libcli.ConfigKey {
	return libcli.ConfigKey{
		Name:        name,
		Description: desc,
		Get: func() (string, bool) {
			cfg := config.Read()
			return format(*field(&cfg))
		},
		Set: func(value string) error {
			v, err := parse(value)
			if err != nil {
				return err
			}
			return config.Update(func(cfg *config.Config) error {
				*field(cfg) = v
				return nil
			})
		},
		Unset: func() error {
			return config.Update(func(cfg *config.Config) error {
				*field(cfg) = unset
				return nil
			})
		},
	}
}

// stringKey builds a ConfigKey over a string field; validate may be nil.
func stringKey(name, desc string, field func(*config.Config) *string, validate func(string) error) libcli.ConfigKey {
	return configKeyOf(name, desc, field,
		func(v string) (string, error) {
			if validate != nil {
				if err := validate(v); err != nil {
					return "", err
				}
			}
			return v, nil
		},
		func(v string) (string, bool) { return v, v != "" },
		"")
}

func optionalBoolKey(name, desc string, field func(*config.Config) **bool) libcli.ConfigKey {
	return configKeyOf(name, desc, field,
		func(v string) (*bool, error) {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, output.New("Value must be true or false, got "+v, output.FixableByAgent)
			}
			return &b, nil
		},
		func(p *bool) (string, bool) {
			if p == nil {
				return "", false
			}
			return strconv.FormatBool(*p), true
		},
		nil)
}

func intKey(name, desc string, field func(*config.Config) *int, min, max int) libcli.ConfigKey {
	return configKeyOf(name, desc, field,
		func(v string) (int, error) { return parseBoundedInt(v, min, max) },
		func(v int) (string, bool) {
			if v == 0 {
				return "", false
			}
			return strconv.Itoa(v), true
		},
		0)
}

// optionalIntKey is intKey over a nullable field: unset restores the coded
// default (nil), and an explicit value (including 0) is stored as set.
func optionalIntKey(name, desc string, field func(*config.Config) **int, min, max int) libcli.ConfigKey {
	return configKeyOf(name, desc, field,
		func(v string) (*int, error) {
			n, err := parseBoundedInt(v, min, max)
			if err != nil {
				return nil, err
			}
			return &n, nil
		},
		func(p *int) (string, bool) {
			if p == nil {
				return "", false
			}
			return strconv.Itoa(*p), true
		},
		nil)
}

// parseBoundedInt is the shared validation for integer config keys: one
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
	if v == "" || slices.Contains(engineValues, v) {
		return nil
	}
	return output.New(`Unknown engine: `+v+`. Valid: codex`, output.FixableByAgent)
}

func validateSandbox(v string) error {
	if v == "" || slices.Contains(sandboxValues, v) {
		return nil
	}
	return output.New("Invalid sandbox mode: "+v+". Valid: read-only, workspace-write, danger-full-access", output.FixableByAgent)
}

func validateTailscaleMode(v string) error {
	if v == "" || slices.Contains(tailscaleModeValues, v) {
		return nil
	}
	return output.New(`Invalid tailscale mode: `+v+`. Valid: "", serve, funnel`, output.FixableByAgent)
}
