package usage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// cacheFetching is a Cache whose polls call fetch instead of a real CLI.
func cacheFetching(fetch func(context.Context, Source) (Snapshot, error)) *Cache {
	c := NewCache()
	c.fetch = fetch
	return c
}

func failingFetch(context.Context, Source) (Snapshot, error) {
	return Snapshot{}, errors.New("engine unavailable")
}

func TestCachePollRecordsFetchFailures(t *testing.T) {
	cache := cacheFetching(failingFetch)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cache.Poll(ctx, time.Hour, Source{Engine: "codex"})
	// Poll has no completion signal, so this waits on the observable effect.
	// The loop exits the moment the error lands, so a generous ceiling costs
	// nothing except when genuinely broken.
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
	// What is under test is slot separation, not retrieval: one engine's
	// fetch works and the other's fails.
	cache := cacheFetching(func(ctx context.Context, src Source) (Snapshot, error) {
		if src.Engine == "broken" {
			return failingFetch(ctx, src)
		}
		return Snapshot{Plan: "pro", Primary: &Window{UsedPercent: 25, WindowMins: 300, ResetsAt: 123}, FetchedAt: time.Now()}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cache.Poll(ctx, time.Hour, Source{Engine: "codex"})
	go cache.Poll(ctx, time.Hour, Source{Engine: "broken"})

	deadline := time.Now().Add(10 * time.Second)
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

// TestCacheIsSafeForConcurrentUse exercises the invariant this type's own doc
// comment claims — "the dashboard reads it while the daemon's refresh loop
// writes" — which nothing had ever run under the race detector, because the
// -race target covered only scheduler and cli. Meaningful only under -race;
// harmless without it.
func TestCacheIsSafeForConcurrentUse(t *testing.T) {
	c := cacheFetching(failingFetch)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	// The daemon's refresh loop.
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.Poll(ctx, time.Millisecond, Source{Engine: "codex"})
	}()

	// Concurrent dashboard requests, reading both shapes the handlers use.
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 300 {
				_ = c.Get("codex")
				_ = c.All()
			}
		}()
	}
	cancel()
	wg.Wait()
}
