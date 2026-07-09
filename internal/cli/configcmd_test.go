package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
)

// TestConfigKeyValidators pins the shared validation each `config set`
// funnels through: bounds, durations, and the closed enums.
func TestConfigKeyValidators(t *testing.T) {
	if _, err := parseBoundedInt("4", 1, 32); err != nil {
		t.Errorf("in-range int must parse: %v", err)
	}
	for _, bad := range []string{"0", "33", "abc", ""} {
		if _, err := parseBoundedInt(bad, 1, 32); err == nil {
			t.Errorf("parseBoundedInt(%q) must fail", bad)
		}
	}

	for _, ok := range []string{"", "30m", "1h30m"} {
		if err := validateDuration(ok); err != nil {
			t.Errorf("validateDuration(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"soon", "-5m", "0s"} {
		if err := validateDuration(bad); err == nil {
			t.Errorf("validateDuration(%q) must fail", bad)
		}
	}

	if err := validateEngine("codex"); err != nil {
		t.Errorf("codex is the valid engine: %v", err)
	}
	if err := validateEngine("claude"); err == nil {
		t.Error("unknown engine must fail until it's wired")
	}
	if err := validateSandbox("workspace-write"); err != nil {
		t.Error("workspace-write is a valid sandbox")
	}
	if err := validateSandbox("yolo"); err == nil {
		t.Error("invalid sandbox must fail")
	}
	if err := validateTailscaleMode("serve"); err != nil {
		t.Error("serve is a valid tailscale mode")
	}
	if err := validateTailscaleMode("open"); err == nil {
		t.Error("invalid tailscale mode must fail")
	}
}

// TestConfigKeysRoundTrip drives every registered key's Set→Get→Unset against
// an isolated config dir (XDG_CONFIG_HOME). It verifies unset both reports as
// absent and removes the persisted leaf, not merely that it returned no error.
func TestConfigKeysRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	samples := map[string]string{
		"gh_user":                             "example-handle",
		"schedule.enabled":                    "true",
		"schedule.interval":                   "45m",
		"discovery.enabled":                   "true",
		"discovery.interval":                  "5m",
		"schedule.max_parallel":               "8",
		"candidates.new_max_age_days":         "7",
		"candidates.refreshed_max_age_days":   "30",
		"candidates.rereview_cooldown":        "2h",
		"candidates.quiet_period":             "0s",
		"review.engine":                       "codex",
		"codex.bin":                           "codex",
		"codex.model":                         "some-model",
		"codex.effort":                        "high",
		"codex.sandbox":                       "read-only",
		"dashboard.addr":                      ":9999",
		"dashboard.tailscale.mode":            "serve",
		"dashboard.usage_poll_interval":       "15m",
		"store.path":                          "/tmp/example.duckdb",
		"schedule.usage_floor.5h_percent":     "25",
		"schedule.usage_floor.weekly_percent": "0",
	}
	for _, key := range configKeys() {
		sample, ok := samples[key.Name]
		if !ok {
			t.Errorf("no sample value for key %q — add one so it stays covered", key.Name)
			continue
		}
		if err := key.Set(sample); err != nil {
			t.Errorf("%s: set %q: %v", key.Name, sample, err)
			continue
		}
		if got, set := key.Get(); !set || got != sample {
			t.Errorf("%s: get = (%q, %v), want (%q, true)", key.Name, got, set, sample)
		}
		if err := key.Unset(); err != nil {
			t.Errorf("%s: unset: %v", key.Name, err)
			continue
		}
		if got, set := key.Get(); set || got != "" {
			t.Errorf("%s: after unset get = (%q, %v), want (\"\", false)", key.Name, got, set)
		}
		assertPersistedKeyUnset(t, key.Name)
	}

	// The file holds only what's still set; resolved defaults fill the rest.
	cfg := config.Read()
	if cfg.GHUser != "" || cfg.Schedule.Interval != "" {
		t.Errorf("unset keys must clear, got gh_user=%q interval=%q", cfg.GHUser, cfg.Schedule.Interval)
	}
	if cfg.Interval().String() != "1m0s" {
		t.Errorf("cleared interval must resolve to the default, got %s", cfg.Interval())
	}
	if !cfg.ScheduleEnabled() || !cfg.DiscoveryEnabled() {
		t.Errorf("cleared enabled flags must resolve to true, got schedule=%t discovery=%t", cfg.ScheduleEnabled(), cfg.DiscoveryEnabled())
	}
}

func assertPersistedKeyUnset(t *testing.T, key string) {
	t.Helper()
	data, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse persisted config: %v", err)
	}
	var current any = doc
	for _, segment := range strings.Split(key, ".") {
		fields, ok := current.(map[string]any)
		if !ok {
			return
		}
		value, ok := fields[segment]
		if !ok {
			return
		}
		current = value
	}
	t.Errorf("%s remains in persisted config after unset: %s", key, data)
}
