package config

import (
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
	if got := c.Interval(); got != 30*time.Minute {
		t.Errorf("Interval default = %v, want 30m", got)
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
	if got := c.Interval(); got != 30*time.Minute {
		t.Errorf("Interval on garbage = %v, want 30m fallback", got)
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
