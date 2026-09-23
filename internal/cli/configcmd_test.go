package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	libcli "github.com/shhac/lib-agent-cli/cli"

	"github.com/shhac/crew-code-review/internal/config"
	"github.com/shhac/crew-code-review/internal/review"
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

	validateEngine := validateOneOf("engine", engineValues)
	for _, wired := range review.Engines {
		if err := validateEngine(wired); err != nil {
			t.Errorf("%s is a wired engine: %v", wired, err)
		}
	}
	if err := validateEngine("gemini"); err == nil {
		t.Error("unknown engine must fail until it's wired")
	}
	if err := validateEngine(""); err != nil {
		t.Error("empty (unset) must always be allowed")
	}
	if err := validateOneOf("sandbox mode", sandboxValues)("yolo"); err == nil {
		t.Error("invalid sandbox must fail")
	} else if !strings.Contains(err.Error(), "read-only, workspace-write, danger-full-access") {
		t.Errorf("message must list the valid set from the values slice, got %v", err)
	}
}

// TestConfigKeysRoundTrip drives every registered key's Set→Get→Unset against
// an isolated config dir (XDG_CONFIG_HOME). It verifies unset both reports as
// absent and removes the persisted leaf, not merely that it returned no error.
func TestConfigKeysRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	samples := map[string]string{
		"review.workspace_retention":         "168h",
		"gh_user":                            "example-handle",
		"schedule.enabled":                   "true",
		"schedule.interval":                  "45m",
		"schedule.dispatch_cooldown":         "10s",
		"discovery.enabled":                  "true",
		"discovery.interval":                 "5m",
		"discovery.list_limit":               "250",
		"discovery.sweep_budget":             "4m",
		"schedule.max_parallel":              "8",
		"candidates.new_max_age_days":        "7",
		"candidates.refreshed_max_age_days":  "30",
		"candidates.discussion_max_age_days": "7",
		"candidates.rereview_cooldown":       "2h",
		"candidates.quiet_period":            "0s",
		"candidates.steering_hold":           "3m",
		"candidates.error_backoff":           "5m",
		"candidates.require_review_request":  "false",
		"review.engine":                      "codex",
		"codex.bin":                          "codex",
		"codex.model":                        "some-model",
		"codex.effort":                       "high",
		"codex.sandbox":                      "read-only",
		"dashboard.addr":                     ":9999",
		"dashboard.tailscale.mode":           "serve",
		"dashboard.usage_poll_interval":      "15m",
		"store.path":                         "/tmp/example.duckdb",
		"codex.usage_floor.5h_percent":       "25",
		"codex.usage_floor.1w_percent":       "0",
		"claude.usage_floor.5h_percent":      "30",
		"claude.usage_floor.1w_percent":      "5",
		"codex.max_resumes":                  "3",
		"claude.bin":                         "claude",
		"claude.model":                       "opus",
		"claude.effort":                      "high",
		"claude.permission_mode":             "dontAsk",
		"claude.max_budget_usd":              "2.5",
		"claude.max_resumes":                 "3",
		"scoring.mode":                       "leaderboard-only",
		"scoring.piece_lines":                "40",
		"scoring.size_points":                "120",
		"scoring.size_falloff":               "2.5",
		"scoring.removal_points_per_100":     "25",
		"scoring.curve":                      "step",
		"scoring.shrink_bonus":               "1.5",
		"scoring.attempt_decay":              "0.75",
		"scoring.verdicts.approved":          "2",
		"scoring.verdicts.commented":         "0.5",
		// Negative on purpose: the bound has to admit it, or the shipped
		// default for this key could not be typed back in.
		"scoring.verdicts.requested_changes": "-0.5",
		"scoring.use_gitattributes":          "false",
	}
	for _, key := range configKeysFromSpecs(configKeySpecs()) {
		// A section key has no value to round-trip: its set refuses on
		// purpose. It still has to CLEAR, which is the whole reason it is
		// registered, so it gets the second half of this check.
		if _, ok := samples[key.Name]; !ok && key.Set("anything") != nil {
			assertSectionClears(t, key)
			continue
		}
		sample, ok := samples[key.Name]
		if !ok {
			t.Errorf("no sample value for key %q: add one so it stays covered", key.Name)
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
	if cfg.Interval().String() != "30s" {
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

// The wiring, not the mechanism: libcli.WithDocument owns the fallback and is
// tested there. What matters here is that this command passes it, against the
// real schema -- drop the option and these fail.
func TestConfigGetAndUnsetReachUnknownKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := `{"repos":["o/r"],"schedule":{"usage_floor":{"5h_percent":30}}}`
	if err := os.WriteFile(config.Path(), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) error {
		root := newRootCmd("test")
		root.SetArgs(args)
		return root.Execute()
	}

	if err := run("config", "get", "schedule.usage_floor"); err != nil {
		t.Errorf("get on a key the document holds: %v", err)
	}
	// A genuine typo still gets the library's error and its list of valid
	// names; falling through to "unset" would hide the mistake.
	if err := run("config", "get", "schedule.nonsense"); err == nil {
		t.Error("a key in neither the schema nor the document must error")
	}
	if err := run("config", "unset", "schedule.usage_floor"); err != nil {
		t.Errorf("unset on a key the document holds: %v", err)
	}
	if got := config.UnknownKeys(); len(got) != 0 {
		t.Errorf("the key survived the unset: %+v", got)
	}
	// The write must not have gone through the struct, which would drop
	// nothing here but does drop annotations elsewhere.
	if cfg := config.Read(); len(cfg.Repos) != 1 {
		t.Errorf("known settings did not survive: %+v", cfg)
	}
}

// `set` is deliberately not extended to unknown keys: writing a key nothing
// reads would manufacture the state the warning exists to clear.
func TestConfigSetRefusesUnknownKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(), []byte(`{"schedule":{"usage_floor":{"5h_percent":30}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	root := newRootCmd("test")
	root.SetArgs([]string{"config", "set", "schedule.usage_floor", "20"})
	if err := root.Execute(); err == nil {
		t.Error("set on an unknown key must fail even when the document holds it")
	}
}

// assertSectionClears is the round-trip a section key can support: set the
// keys inside it, then unset the section and find them gone. Without this a
// section key would be registered and never exercised.
func assertSectionClears(t *testing.T, key libcli.ConfigKey) {
	t.Helper()
	inner := key.Name + ".5h_percent"
	for _, spec := range configKeySpecs() {
		if spec.key.Name != inner {
			continue
		}
		if err := spec.key.Set("30"); err != nil {
			t.Fatalf("%s: %v", inner, err)
		}
	}
	if got, set := key.Get(); !set || !strings.Contains(got, "30") {
		t.Errorf("%s: get = (%q, %v), want the windows it holds", key.Name, got, set)
	}
	if err := key.Unset(); err != nil {
		t.Fatalf("%s: unset: %v", key.Name, err)
	}
	if got, set := key.Get(); set || got != "" {
		t.Errorf("%s: after unset get = (%q, %v), want cleared", key.Name, got, set)
	}
	assertPersistedKeyUnset(t, inner)
}
