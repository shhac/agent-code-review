package config

import "testing"

func TestUsageFloorDefaults(t *testing.T) {
	var c Config
	if c.UsageFloor5h() != 10 || c.UsageFloorWeekly() != 10 {
		t.Fatalf("unset floors must default to 10, got %d/%d", c.UsageFloor5h(), c.UsageFloorWeekly())
	}
	zero, thirty := 0, 30
	c.Schedule.UsageFloor.FiveHourPercent = &zero
	c.Schedule.UsageFloor.WeeklyPercent = &thirty
	if c.UsageFloor5h() != 0 {
		t.Error("explicit 0 must be honored (disables the floor)")
	}
	if c.UsageFloorWeekly() != 30 {
		t.Error("explicit value must be honored")
	}
}
