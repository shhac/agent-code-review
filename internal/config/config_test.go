package config

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	var c Config
	if got := c.NewMaxAge(); got != 14*24*time.Hour {
		t.Errorf("NewMaxAge default = %v, want 14d", got)
	}
	if got := c.RefreshedMaxAge(); got != 21*24*time.Hour {
		t.Errorf("RefreshedMaxAge default = %v, want 21d", got)
	}
	if got := c.MaxParallel(); got != 4 {
		t.Errorf("MaxParallel default = %d, want 4", got)
	}
	if got := c.Interval(); got != time.Minute {
		t.Errorf("Interval default = %v, want 1m", got)
	}
	if got := c.RereviewCooldown(); got != 90*time.Minute {
		t.Errorf("RereviewCooldown default = %v, want 90m", got)
	}
	if got := c.QuietPeriod(); got != 15*time.Minute {
		t.Errorf("QuietPeriod default = %v, want 15m", got)
	}
	if got := c.Engine(); got != "codex" {
		t.Errorf("Engine default = %q, want codex", got)
	}
	if got := c.DashboardAddr(); got != ":8330" {
		t.Errorf("DashboardAddr default = %q, want :8330", got)
	}
}

func TestOverrides(t *testing.T) {
	c := Config{
		Candidates: CandidateSettings{NewMaxAgeDays: 7, RefreshedMaxAgeDays: 10},
		Schedule:   ScheduleSettings{Interval: "5m", MaxParallel: 2},
		Review:     ReviewSettings{Engine: "claude"},
	}
	if got := c.NewMaxAge(); got != 7*24*time.Hour {
		t.Errorf("NewMaxAge = %v, want 7d", got)
	}
	if got := c.Interval(); got != 5*time.Minute {
		t.Errorf("Interval = %v, want 5m", got)
	}
	if got := c.MaxParallel(); got != 2 {
		t.Errorf("MaxParallel = %d, want 2", got)
	}
	if got := c.Engine(); got != "claude" {
		t.Errorf("Engine = %q, want claude", got)
	}
}

func TestIntervalFallsBackOnGarbage(t *testing.T) {
	c := Config{Schedule: ScheduleSettings{Interval: "not-a-duration"}}
	if got := c.Interval(); got != time.Minute {
		t.Errorf("Interval on garbage = %v, want 1m fallback", got)
	}
}

// TestHoldGetters pins the eligibility-hold semantics: overrides apply, an
// explicit "0s" DISABLES a hold (unlike the interval dials, where zero falls
// back), and garbage falls back to the default.
func TestHoldGetters(t *testing.T) {
	c := Config{Candidates: CandidateSettings{RereviewCooldown: "2h", QuietPeriod: "5m"}}
	if got := c.RereviewCooldown(); got != 2*time.Hour {
		t.Errorf("RereviewCooldown override = %v, want 2h", got)
	}
	if got := c.QuietPeriod(); got != 5*time.Minute {
		t.Errorf("QuietPeriod override = %v, want 5m", got)
	}
	c = Config{Candidates: CandidateSettings{RereviewCooldown: "0s", QuietPeriod: "0s"}}
	if got := c.RereviewCooldown(); got != 0 {
		t.Errorf("RereviewCooldown 0s = %v, want 0 (disabled)", got)
	}
	if got := c.QuietPeriod(); got != 0 {
		t.Errorf("QuietPeriod 0s = %v, want 0 (disabled)", got)
	}
	c = Config{Candidates: CandidateSettings{RereviewCooldown: "junk", QuietPeriod: "-1m"}}
	if got := c.RereviewCooldown(); got != 90*time.Minute {
		t.Errorf("RereviewCooldown garbage = %v, want 90m fallback", got)
	}
	if got := c.QuietPeriod(); got != 15*time.Minute {
		t.Errorf("QuietPeriod negative = %v, want 15m fallback", got)
	}
}

func TestDurationGetters(t *testing.T) {
	var zero Config
	if got := zero.DiscoverInterval(); got != 10*time.Minute {
		t.Errorf("DiscoverInterval default = %v, want 10m", got)
	}
	if got := zero.UsagePollInterval(); got != 10*time.Minute {
		t.Errorf("UsagePollInterval default = %v, want 10m", got)
	}
	c := Config{Discovery: DiscoverySettings{Interval: "5m"}}
	if got := c.DiscoverInterval(); got != 5*time.Minute {
		t.Errorf("DiscoverInterval override = %v, want 5m", got)
	}
	c.Discovery.Interval = "-3m" // non-positive falls back
	if got := c.DiscoverInterval(); got != 10*time.Minute {
		t.Errorf("DiscoverInterval negative = %v, want 10m fallback", got)
	}
}

func TestRepoPredicates(t *testing.T) {
	c := Config{
		Repos:                   []string{"Org/Repo"},
		AllowedAuthorsOnlyRepos: []string{"org/scoped"},
	}
	if !c.WatchesRepo("org/repo") || c.WatchesRepo("org/other") {
		t.Error("WatchesRepo must be case-insensitive and exact-membership")
	}
	if !c.AuthorScopedRepo("ORG/SCOPED") || c.AuthorScopedRepo("org/repo") {
		t.Error("AuthorScopedRepo must be case-insensitive and exact-membership")
	}
	if !RepoMatches([]string{"Example/Repo"}, "example/repo") || RepoMatches([]string{"example/repo"}, "example/other") {
		t.Error("RepoMatches must centralize case-insensitive repo membership")
	}
}

func TestSortedRepos(t *testing.T) {
	cfg := Config{Repos: []string{"zeta/api", "Alpha/web", "alpha/admin"}}
	got := cfg.SortedRepos()
	want := []string{"alpha/admin", "Alpha/web", "zeta/api"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Config.SortedRepos = %v, want %v", got, want)
	}
	if strings.Join(cfg.Repos, ",") != "zeta/api,Alpha/web,alpha/admin" {
		t.Errorf("Config.SortedRepos must not mutate input, got %v", cfg.Repos)
	}
}

func TestValidRepoName(t *testing.T) {
	for _, ok := range []string{"owner/repo", "o-w.n_er/r.e-p_o1"} {
		if !ValidRepoName(ok) {
			t.Errorf("ValidRepoName(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "owner", "owner/repo/extra", "own er/repo", "owner/"} {
		if ValidRepoName(bad) {
			t.Errorf("ValidRepoName(%q) = true, want false", bad)
		}
	}
}

// TestStarterMatchesExample keeps the embedded starter (written by `config
// init`) in lockstep with the repo's documented config.example.json.
func TestStarterMatchesExample(t *testing.T) {
	example, err := os.ReadFile("../../config.example.json")
	if err != nil {
		t.Fatalf("read config.example.json: %v", err)
	}
	if !bytes.Equal(bytes.TrimSpace(example), bytes.TrimSpace(StarterJSON())) {
		t.Error("internal/config/starter.json and config.example.json have drifted — keep them identical")
	}
	// The starter must also parse as a Config (annotation keys like //rules_note
	// are ignored by encoding/json, but the structure must be valid).
	var cfg Config
	if err := json.Unmarshal(StarterJSON(), &cfg); err != nil {
		t.Fatalf("starter.json does not parse as Config: %v", err)
	}
	if len(cfg.Repos) != 0 {
		t.Error("starter must ship with NO repos — watched repos are configured, never placeholder")
	}
	if cfg.Review.MainPrompt == "" {
		t.Error("starter should ship a generic main prompt")
	}
	// The shipped prompt must assume only gh + codex — no skills or extra CLIs.
	lower := strings.ToLower(cfg.Review.MainPrompt + cfg.Review.OnApprove + cfg.Review.OnComment + cfg.Review.OnReject)
	for _, banned := range []string{"pr-issue-review", "agent-slack", "slack", "emoji"} {
		if strings.Contains(lower, banned) {
			t.Errorf("starter prompts must not assume %q — that's user-config territory", banned)
		}
	}
}

func TestStorePathDefaultsUnderDataDir(t *testing.T) {
	var c Config
	if got := c.StorePath(); !strings.HasSuffix(got, "queue.duckdb") {
		t.Errorf("StorePath default = %q, want …/queue.duckdb", got)
	}
	c.Store.Path = "/tmp/custom.duckdb"
	if got := c.StorePath(); got != "/tmp/custom.duckdb" {
		t.Errorf("StorePath override = %q, want /tmp/custom.duckdb", got)
	}
}

// TestInitAndReadWrite pins the file lifecycle under an isolated config dir:
// Init writes the starter exactly once (refusing to overwrite a real
// config), Read parses what Init wrote, and Write→Read round-trips edits.
func TestInitAndReadWrite(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	path, err := Init()
	if err != nil {
		t.Fatalf("first Init must succeed: %v", err)
	}
	if path != Path() {
		t.Errorf("Init path = %q, want %q", path, Path())
	}
	if _, err := Init(); err == nil {
		t.Fatal("second Init must refuse to overwrite")
	}

	cfg := Read()
	if cfg.Schedule.Interval != "1m" {
		t.Errorf("starter schedule.interval = %q, want 1m", cfg.Schedule.Interval)
	}

	cfg.GHUser = "example-handle"
	if err := Write(cfg); err != nil {
		t.Fatal(err)
	}
	if got := Read(); got.GHUser != "example-handle" {
		t.Errorf("round-trip gh_user = %q", got.GHUser)
	}
}
