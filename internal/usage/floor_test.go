package usage

import (
	"strings"
	"testing"
	"time"
)

func snap(primaryUsed, weeklyUsed float64) Snapshot {
	return Snapshot{
		FetchedAt: time.Now(),
		Primary:   &Window{UsedPercent: primaryUsed, WindowMins: 300},
		Secondary: &Window{UsedPercent: weeklyUsed, WindowMins: 10080},
	}
}

func TestBelowFloor(t *testing.T) {
	cases := []struct {
		name         string
		s            Snapshot
		f5h, fweekly int
		want         bool
		wantWindow   string
	}{
		{"plenty of headroom", snap(50, 50), 10, 10, false, ""},
		{"5h window trips", snap(95, 50), 10, 10, true, "5h"},
		{"weekly window trips", snap(50, 95), 10, 10, true, "weekly"},
		{"exactly at floor does not trip", snap(90, 90), 10, 10, false, ""},
		{"zero disables the 5h floor", snap(99, 50), 0, 10, false, ""},
		{"zero disables the weekly floor", snap(50, 99), 10, 0, false, ""},
		{"empty snapshot fails open", Snapshot{}, 10, 10, false, ""},
		{"errored snapshot fails open", Snapshot{FetchedAt: time.Now(), Error: "boom", Primary: &Window{UsedPercent: 99, WindowMins: 300}}, 10, 10, false, ""},
		{"missing windows fail open", Snapshot{FetchedAt: time.Now()}, 10, 10, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := BelowFloor(tc.s, tc.f5h, tc.fweekly)
			if got != tc.want {
				t.Fatalf("BelowFloor = %v (%q), want %v", got, reason, tc.want)
			}
			if tc.want && !strings.HasPrefix(reason, tc.wantWindow) {
				t.Errorf("reason %q should name the %s window", reason, tc.wantWindow)
			}
		})
	}
}

// Windows are matched by duration, not slot: a weekly window reported in the
// primary slot must still use the weekly floor.
func TestBelowFloorMatchesByDuration(t *testing.T) {
	s := Snapshot{
		FetchedAt: time.Now(),
		Primary:   &Window{UsedPercent: 95, WindowMins: 10080},
	}
	paused, reason := BelowFloor(s, 0, 10)
	if !paused || !strings.HasPrefix(reason, "weekly") {
		t.Fatalf("weekly-length window in the primary slot must use the weekly floor, got %v (%q)", paused, reason)
	}
}
