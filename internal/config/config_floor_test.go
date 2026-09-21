package config

import "testing"

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
