package config

import (
	"strings"
	"testing"
)

func pct(n int) *int { return &n }

func TestUsageFloorDefaults(t *testing.T) {
	var c Config
	for _, engine := range []string{"codex", "claude"} {
		if fiveH, oneW := c.Review.UsageFloors(engine); fiveH != 10 || oneW != 10 {
			t.Fatalf("%s: unset floors must default to 10, got %d/%d", engine, fiveH, oneW)
		}
	}
	c.Review.Codex.UsageFloor.FiveHourPercent = pct(0)
	c.Review.Codex.UsageFloor.OneWeekPercent = pct(30)
	fiveH, oneW := c.Review.UsageFloors("codex")
	if fiveH != 0 {
		t.Error("explicit 0 must be honored (disables the floor)")
	}
	if oneW != 30 {
		t.Error("explicit value must be honored")
	}
	// The whole point of the move off schedule.usage_floor: one engine's
	// account running dry says nothing about the other's.
	if fiveH, oneW := c.Review.UsageFloors("claude"); fiveH != 10 || oneW != 10 {
		t.Errorf("codex's floors leaked onto claude: got %d/%d, want the defaults", fiveH, oneW)
	}
}

// An engine nobody recognises resolves to the default engine's block, the
// same fallback BinFor has always had, rather than silently metering at 10.
func TestUsageFloorUnknownEngineFallsBackToCodex(t *testing.T) {
	var c Config
	c.Review.Codex.UsageFloor.FiveHourPercent = pct(42)
	if fiveH, _ := c.Review.UsageFloors("gemini"); fiveH != 42 {
		t.Errorf("unknown engine floor = %d, want codex's 42", fiveH)
	}
}

// A cohort's floors are keyed by ENGINE, so moving the cohort onto the other
// engine does not change how much headroom it leaves on either.
func TestPolicyUsageFloorsAreKeyedByEngine(t *testing.T) {
	c := Config{}
	c.Authors.Groups = map[string]Group{"core": {
		Engine: "claude",
		UsageFloor: map[string]UsageFloorLimits{
			"codex":  {FiveHourPercent: pct(40)},
			"claude": {OneWeekPercent: pct(2)},
		},
	}}
	p := c.ResolvePolicy("acme/widgets", "ada", Membership{Group: "core"})

	r := c.Review.WithPolicy(p)
	if fiveH, oneW := r.UsageFloors("claude"); fiveH != 10 || oneW != 2 {
		t.Errorf("claude floors = %d/%d, want 10/2 (5h inherits, 1w overridden)", fiveH, oneW)
	}
	// Set on the engine the group is NOT using: it must still be there, or
	// switching the group's engine would silently change its headroom.
	if fiveH, oneW := r.UsageFloors("codex"); fiveH != 40 || oneW != 10 {
		t.Errorf("codex floors = %d/%d, want 40/10", fiveH, oneW)
	}
	// The base settings are untouched: WithPolicy returns a patched copy.
	if fiveH, _ := c.Review.UsageFloors("codex"); fiveH != 10 {
		t.Errorf("the policy patched the base config: codex 5h = %d, want 10", fiveH)
	}
}

// An override narrows the group window by window, so it can raise one floor
// without clearing what the group said about the other.
func TestOverrideUsageFloorMergesWindowByWindow(t *testing.T) {
	c := Config{}
	c.Authors.Groups = map[string]Group{"core": {
		UsageFloor: map[string]UsageFloorLimits{"codex": {FiveHourPercent: pct(40), OneWeekPercent: pct(35)}},
	}}
	c.Authors.Overrides = []AuthorOverride{{
		Handle: "ada",
		Group:  Group{UsageFloor: map[string]UsageFloorLimits{"codex": {OneWeekPercent: pct(5)}}},
	}}
	p := c.ResolvePolicy("acme/widgets", "ada", Membership{Group: "core"})
	if fiveH, oneW := c.Review.WithPolicy(p).UsageFloors("codex"); fiveH != 40 || oneW != 5 {
		t.Errorf("floors = %d/%d, want 40/5 (group's 5h survives the override)", fiveH, oneW)
	}
}

// The cascade explains itself, so an unexpected hold can be traced to the
// layer that set the floor rather than guessed at.
func TestExplainPolicyTracesUsageFloors(t *testing.T) {
	c := Config{}
	c.Authors.Groups = map[string]Group{"core": {
		UsageFloor: map[string]UsageFloorLimits{"claude": {OneWeekPercent: pct(2)}},
	}}
	_, trace := c.ExplainPolicy("acme/widgets", "ada", Membership{Group: "core"})
	var found bool
	for _, step := range trace {
		if step.Field == "usage_floor.claude.1w_percent" {
			found = true
			if step.Value != "2" || step.Source != "group[core]" {
				t.Errorf("floor step = %+v, want value 2 from group[core]", step)
			}
		}
	}
	if !found {
		t.Errorf("no usage_floor step in the trace: %+v", trace)
	}
}

func TestLeaseWindowFloor(t *testing.T) {
	var c Config // default 30m interval -> 2h floor wins
	if got := c.LeaseWindow(); got.Hours() != 2 {
		t.Errorf("default lease window = %v, want 2h floor", got)
	}
	c.Schedule.Interval = "15m"
	if got := c.LeaseWindow(); got.Hours() != 2 {
		t.Errorf("15m interval must keep the 2h floor, got %v", got)
	}
	c.Schedule.Interval = "1h30m"
	if got := c.LeaseWindow(); got.Hours() != 6 {
		t.Errorf("1h30m interval lease window = %v, want 6h", got)
	}
}

// An engine name nothing recognises must be REPORTED, not silently applied to
// the default engine. EngineCommon's fallback is right for BinFor and wrong
// for a gate on spend: a cohort writing "Claude" would otherwise move codex's
// floor, and the pause reason would name an engine the author never mentioned.
func TestValidateReportsUnrecognisedFloorEngine(t *testing.T) {
	c := Config{}
	c.Authors.Groups = map[string]Group{"core": {
		UsageFloor: map[string]UsageFloorLimits{"Claude": {OneWeekPercent: pct(90)}},
	}}
	problems := strings.Join(c.ValidateAuthors(), "; ")
	if !strings.Contains(problems, `"Claude"`) || !strings.Contains(problems, "authors.groups.core.usage_floor") {
		t.Errorf("an unrecognised engine section must be reported, got %q", problems)
	}
}

// BelowFloor gates on `floor > 0`, so a negative percentage disables the
// window rather than being rejected -- the quota gate silently off, which is
// one of the two failure modes this feature exists to prevent. Cohort floors
// have no CLI bound, so this validator is the only thing that says it.
func TestValidateReportsOutOfRangeFloor(t *testing.T) {
	for _, bad := range []int{-5, 101} {
		c := Config{}
		c.Authors.Groups = map[string]Group{"core": {
			UsageFloor: map[string]UsageFloorLimits{"codex": {FiveHourPercent: pct(bad)}},
		}}
		problems := strings.Join(c.ValidateAuthors(), "; ")
		if !strings.Contains(problems, "usage_floor.codex.5h_percent") {
			t.Errorf("floor %d must be reported, got %q", bad, problems)
		}
	}
	// The two meaningful ends stay legal: 0 disables, 100 always holds.
	for _, ok := range []int{0, 100} {
		c := Config{}
		c.Authors.Groups = map[string]Group{"core": {
			UsageFloor: map[string]UsageFloorLimits{"codex": {FiveHourPercent: pct(ok)}},
		}}
		if problems := c.ValidateAuthors(); len(problems) != 0 {
			t.Errorf("floor %d must be accepted, got %v", ok, problems)
		}
	}
}

// An override is a group patch, so it carries the same dial and needs the
// same check; nothing else validates it.
func TestValidateReportsOverrideFloorProblems(t *testing.T) {
	c := Config{}
	c.Authors.Overrides = []AuthorOverride{{
		Handle: "ada",
		Group:  Group{UsageFloor: map[string]UsageFloorLimits{"gemini": {OneWeekPercent: pct(5)}}},
	}}
	problems := strings.Join(c.ValidateAuthors(), "; ")
	if !strings.Contains(problems, "authors.overrides[0].usage_floor") {
		t.Errorf("an override's floors must be validated too, got %q", problems)
	}
}

// The base engine is the one every cohort falls back to, and it was the only
// engine name nothing checked: a typo there parsed, saved and loaded, then
// surfaced as a missing binary rather than as a bad name.
func TestValidateReportsUnwiredBaseEngine(t *testing.T) {
	c := Config{Review: ReviewSettings{Engine: "gemini"}}
	problems := strings.Join(c.ValidateReview(), "; ")
	if !strings.Contains(problems, "review.engine") || !strings.Contains(problems, "gemini") {
		t.Errorf("an unwired base engine must be reported, got %q", problems)
	}
	// Empty means "use the default", which is not a typo.
	if problems := (Config{}).ValidateReview(); len(problems) != 0 {
		t.Errorf("an unset engine must be accepted, got %v", problems)
	}
	for _, wired := range EngineNames {
		c := Config{Review: ReviewSettings{Engine: wired}}
		if problems := c.ValidateReview(); len(problems) != 0 {
			t.Errorf("%s is wired: %v", wired, problems)
		}
	}
}
