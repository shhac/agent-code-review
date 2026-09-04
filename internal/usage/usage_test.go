package usage

import (
	"context"
	"testing"
	"time"
)

func TestCachePollRecordsFetchFailures(t *testing.T) {
	cache := NewCache()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cache.Poll(ctx, time.Hour, Source{Engine: "codex", Bin: fakeCodex(t, "exit 12")})
	// Poll has no completion signal, so this waits on the observable effect.
	// The ceiling is generous because the first fetch spawns a subprocess:
	// a one-second budget passed alone and missed under -race with the whole
	// suite competing for the machine. The loop exits the moment the error
	// lands, so a large ceiling costs nothing except when genuinely broken.
	deadline := time.Now().Add(10 * time.Second)
	for cache.Get("codex").Error == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if snap := cache.Get("codex"); snap.Error == "" || snap.FetchedAt.IsZero() {
		t.Errorf("failed poll snapshot = %+v", snap)
	}
}

// A failed poll still stamps FetchedAt, so "we tried" and "we have numbers"
// must be different questions: without OK() the dashboard renders an empty
// meter and never says why.
func TestSnapshotOK(t *testing.T) {
	stamped := time.Now()
	for name, tc := range map[string]struct {
		snap Snapshot
		want bool
	}{
		"never polled": {Snapshot{}, false},
		"errored":      {Snapshot{Error: "codex not on PATH", FetchedAt: stamped}, false},
		"no windows":   {Snapshot{FetchedAt: stamped}, false},
		"usable":       {Snapshot{FetchedAt: stamped, Primary: &Window{UsedPercent: 3, WindowMins: 300}}, true},
		"weekly only":  {Snapshot{FetchedAt: stamped, Secondary: &Window{UsedPercent: 8, WindowMins: 10080}}, true},
	} {
		if got := tc.snap.OK(); got != tc.want {
			t.Errorf("%s: OK() = %v, want %v", name, got, tc.want)
		}
	}
}

// Each engine gets its own slot: one engine failing must not blank the other,
// which is the whole point of showing them side by side.
func TestCacheKeepsEnginesSeparate(t *testing.T) {
	cache := NewCache()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Both sources take the codex path (an unrecognised engine falls back to
	// it), so the test stays offline: the claude reader would hit the real
	// OAuth endpoint. What is under test is slot separation, not retrieval.
	go cache.Poll(ctx, time.Hour, Source{Engine: "codex", Bin: fakeCodex(t, `printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"rateLimits":{"planType":"pro","primary":{"usedPercent":25,"windowDurationMins":300,"resetsAt":123}}}}'`)})
	go cache.Poll(ctx, time.Hour, Source{Engine: "broken", Bin: fakeCodex(t, "exit 12")})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cache.Get("codex").OK() && cache.Get("broken").Error != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if got := cache.Get("codex"); !got.OK() || got.Plan != "pro" {
		t.Errorf("codex slot = %+v, want a usable snapshot", got)
	}
	if got := cache.Get("broken"); got.OK() || got.Error == "" {
		t.Errorf("broken slot = %+v, want a recorded failure", got)
	}
	if all := cache.All(); len(all) != 2 {
		t.Errorf("All() = %v, want both engines", all)
	}
}
