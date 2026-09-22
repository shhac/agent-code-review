package usage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shhac/lib-agent-harness/session"
)

func quotaWindow(id string, used float64, mins int64) session.QuotaWindow {
	resets := time.Unix(1790175224, 0).UTC()
	return session.QuotaWindow{ID: id, UsedPercent: &used, WindowMinutes: &mins, ResetsAt: &resets}
}

func measured(windows ...session.QuotaWindow) session.QuotaSnapshot {
	return session.QuotaSnapshot{
		Observation: session.Observation{Quality: session.Measured, ObservedAt: time.Now()},
		Complete:    true,
		Windows:     windows,
	}
}

// Inspect reports every window an engine exposes. Only the account-wide pair
// may reach the floor: a scoped window near its limit (a per-model weekly
// cap, or codex's separate review bucket, which the harness's own fixture
// shows past 100%) is not what a review spends from, and letting it through
// would park reviews over it.
func TestFromInspectionKeepsOnlyTheAccountWindows(t *testing.T) {
	for name, tc := range map[string]struct {
		engine        session.Engine
		windows       []session.QuotaWindow
		primary, week float64
	}{
		"claude": {session.Claude, []session.QuotaWindow{
			quotaWindow("five_hour", 30, 300),
			quotaWindow("model:Fable", 99, 10080),
			quotaWindow("seven_day", 40, 10080),
			quotaWindow("seven_day_opus", 99, 10080),
		}, 30, 40},
		"codex": {session.Codex, []session.QuotaWindow{
			quotaWindow("codex/primary", 25, 300),
			quotaWindow("codex/secondary", 60, 10080),
			quotaWindow("review/primary", 123, 300),
		}, 25, 60},
		"codex before per-limit buckets": {session.Codex, []session.QuotaWindow{
			quotaWindow("default/primary", 10, 300),
			quotaWindow("default/secondary", 20, 10080),
		}, 10, 20},
	} {
		t.Run(name, func(t *testing.T) {
			snap, err := fromInspection(tc.engine, session.Inspection{Quota: measured(tc.windows...)}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if snap.Primary == nil || snap.Primary.UsedPercent != tc.primary || snap.Secondary == nil || snap.Secondary.UsedPercent != tc.week {
				t.Fatalf("snapshot = %+v / %+v, want %v / %v", snap.Primary, snap.Secondary, tc.primary, tc.week)
			}
			if below, why := BelowFloor(snap, 20, 20); below {
				t.Errorf("a scoped window tripped the floor: %s", why)
			}
		})
	}
}

func TestFromInspectionMapsAWindow(t *testing.T) {
	in := session.Inspection{
		Account: session.AccountSnapshot{Plan: "prolite"},
		Quota:   measured(quotaWindow("codex/primary", 96, 10080)),
	}
	snap, err := fromInspection(session.Codex, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Window{UsedPercent: 96, WindowMins: 10080, ResetsAt: 1790175224}
	if snap.Plan != "prolite" || snap.Primary == nil || *snap.Primary != want || snap.Secondary != nil || !snap.OK() {
		t.Errorf("snapshot = %+v primary %+v, want plan prolite and %+v alone", snap, snap.Primary, want)
	}

	// Unreported percent and reset read as zero, as the old readers did; a
	// window with no duration cannot be placed as 5-hourly or weekly, so it
	// is not mapped at all.
	mins := int64(300)
	bare := session.Inspection{Quota: measured(
		session.QuotaWindow{ID: "five_hour", WindowMinutes: &mins},
		session.QuotaWindow{ID: "seven_day"},
	)}
	snap, _ = fromInspection(session.Claude, bare, nil)
	if snap.Primary == nil || *snap.Primary != (Window{WindowMins: 300}) || snap.Secondary != nil {
		t.Errorf("bare windows = %+v / %+v", snap.Primary, snap.Secondary)
	}
}

// Inspect returns what it got alongside the joined error, so an account read
// that failed must not blank a quota that arrived.
func TestFromInspectionMetersDespiteAFailedAccountRead(t *testing.T) {
	in := session.Inspection{Quota: measured(quotaWindow("five_hour", 5, 300))}
	snap, err := fromInspection(session.Claude, in, errors.Join(session.ErrProtocol, nil))
	if err != nil || !snap.OK() {
		t.Errorf("snapshot = %+v, err = %v, want a usable snapshot", snap, err)
	}
}

// Every way of getting no headroom is an error, so Poll records it and the
// floor fails open; the text is what the dashboard shows beside the empty
// meter.
func TestFromInspectionExplainsNoHeadroom(t *testing.T) {
	loggedOut := false
	for name, tc := range map[string]struct {
		engine session.Engine
		in     session.Inspection
		err    error
		want   string
	}{
		"logged out":           {session.Codex, session.Inspection{Account: session.AccountSnapshot{LoggedIn: &loggedOut}}, nil, "run `codex login`"},
		"harness error":        {session.Claude, session.Inspection{}, session.ErrUnsupported, "claude usage: harness operation unsupported"},
		"provider had nothing": {session.Claude, session.Inspection{Quota: session.QuotaSnapshot{Observation: session.Observation{Reason: "rate limits unavailable"}}}, nil, "rate limits unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			snap, err := fromInspection(tc.engine, tc.in, tc.err)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Errorf("err = %v, want it to wrap %v", err, tc.err)
			}
			if below, _ := BelowFloor(snap, 99, 99); below {
				t.Error("an unavailable meter paused reviews; it must fail open")
			}
		})
	}
}

func TestFetchAsksTheSourcesEngine(t *testing.T) {
	var asked []session.Options
	was := inspect
	t.Cleanup(func() { inspect = was })
	inspect = func(_ context.Context, o session.Options) (session.Inspection, error) {
		asked = append(asked, o)
		return session.Inspection{Quota: measured(quotaWindow("five_hour", 1, 300), quotaWindow("codex/primary", 1, 300))}, nil
	}

	for _, src := range []Source{{Engine: "claude", Bin: "/opt/claude"}, {Engine: "codex"}, {Engine: "something-else"}} {
		if snap, err := Fetch(t.Context(), src); err != nil || !snap.OK() {
			t.Errorf("Fetch(%+v) = %+v, %v", src, snap, err)
		}
	}
	want := []session.Options{{Engine: session.Claude, Binary: "/opt/claude"}, {Engine: session.Codex}, {Engine: session.Codex}}
	if len(asked) != len(want) {
		t.Fatalf("asked %d times, want %d", len(asked), len(want))
	}
	for i := range want {
		if asked[i].Engine != want[i].Engine || asked[i].Binary != want[i].Binary {
			t.Errorf("call %d = %s %q, want %s %q (an unknown engine falls back to codex)", i, asked[i].Engine, asked[i].Binary, want[i].Engine, want[i].Binary)
		}
	}
}
