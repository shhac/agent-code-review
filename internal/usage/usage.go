// Package usage reads the review engine's remaining subscription headroom, so
// the scheduler can leave room for interactive work (see BelowFloor) and the
// dashboard can show it. Every engine reports through the same Snapshot; only
// the retrieval differs, because each vendor exposes it differently:
//
//	codex   spawn `codex app-server` and speak JSON-RPC over stdio, the way
//	        the desktop app does (initialize → account/rateLimits/read); see
//	        codex.go
//	claude  read the account's OAuth usage endpoint; see claude.go
//
// A Cache polls on an interval so the dashboard needs no round trip per
// request.
package usage

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Window is one rate-limit window (Codex reports a primary ~5h window and a
// secondary weekly one).
type Window struct {
	UsedPercent float64 `json:"used_percent"`
	WindowMins  int     `json:"window_mins"`
	ResetsAt    int64   `json:"resets_at"` // unix seconds
}

// Snapshot is the dashboard-facing view of Codex usage.
type Snapshot struct {
	Plan      string    `json:"plan,omitempty"`
	Primary   *Window   `json:"primary,omitempty"`
	Secondary *Window   `json:"secondary,omitempty"`
	FetchedAt time.Time `json:"fetched_at"`
	Error     string    `json:"error,omitempty"`
}

// weeklyThresholdMins separates the two Codex windows by duration rather than
// position: a window of at least a week is "weekly", anything shorter is the
// session (5h) window.
const weeklyThresholdMins = 10080

// BelowFloor reports whether review work should pause because a usage
// window's REMAINING percentage has dropped below its floor, and names the
// window that tripped. A floor of 0 disables that window's check. Fail-open
// by design: an empty or errored snapshot never pauses, because review
// availability must not depend on the usage meter working.
func BelowFloor(s Snapshot, floor5h, floorWeekly int) (bool, string) {
	if s.FetchedAt.IsZero() || s.Error != "" {
		return false, ""
	}
	for _, w := range []*Window{s.Primary, s.Secondary} {
		if w == nil {
			continue
		}
		floor, name := floor5h, "5h"
		if w.WindowMins >= weeklyThresholdMins {
			floor, name = floorWeekly, "weekly"
		}
		remaining := 100 - w.UsedPercent
		if floor > 0 && remaining < float64(floor) {
			return true, fmt.Sprintf("%s window has %.0f%% remaining, floor is %d%%", name, remaining, floor)
		}
	}
	return false, ""
}

// Source identifies whose headroom to read: the engine that will spend it,
// and where its CLI lives. Headroom is a property of the account the engine
// bills against, so it has to follow the configured engine rather than being
// polled unconditionally.
type Source struct {
	Engine string // "codex" (default) | "claude"
	Bin    string // that engine's binary; empty means its own default name
}

// Fetch reads one snapshot from the source's engine. An unrecognised engine
// falls back to codex, matching NewEngine's default.
func Fetch(ctx context.Context, src Source) (Snapshot, error) {
	if src.Engine == "claude" {
		return fetchClaude(ctx, src.Bin)
	}
	return fetchCodex(ctx, src.Bin)
}

// OK reports whether the snapshot carries usable headroom. A poll that failed
// still stamps FetchedAt, so "we tried" and "we have numbers" are different
// questions and callers must ask this one before rendering meters.
func (s Snapshot) OK() bool {
	return !s.FetchedAt.IsZero() && s.Error == "" && (s.Primary != nil || s.Secondary != nil)
}

// Cache holds the latest snapshot per engine and refreshes each on an
// interval. Keyed by engine because the dashboard shows every engine's
// headroom side by side, so an operator can see that the engine they are NOT
// using has room before deciding to switch. The floor reads it the same way,
// per candidate: a group can name its own engine, so which account a review
// spends from is a per-candidate answer.
type Cache struct {
	mu    sync.RWMutex
	snaps map[string]Snapshot
}

func NewCache() *Cache { return &Cache{snaps: map[string]Snapshot{}} }

// Get returns one engine's latest snapshot (zero value until its first poll
// lands, or if it is not polled at all).
func (c *Cache) Get(engine string) Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snaps[engine]
}

// All returns a copy of every engine's latest snapshot.
func (c *Cache) All() map[string]Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]Snapshot, len(c.snaps))
	for engine, snap := range c.snaps {
		out[engine] = snap
	}
	return out
}

// Lazy returns a getter that fetches an engine on first ask and caches the
// result for the life of the Cache, a FAILED probe included. That is the whole
// difference from Poll: a daemon retries forever because a logged-out engine
// may come back, while a command that exits wants one probe and an answer.
// Caching the failure as the empty snapshot is what makes the usage floor fail
// open (BelowFloor never pauses on one), so a broken engine degrades to
// reviewing rather than to a run that silently does nothing.
//
// fetch is a parameter because this sits on the money path: it is what stops a
// scheduled run spending from an account the daemon has deliberately parked.
func (c *Cache) Lazy(fetch func(engine string) (Snapshot, error)) func(string) Snapshot {
	return func(engine string) Snapshot {
		c.mu.Lock()
		defer c.mu.Unlock()
		if snap, ok := c.snaps[engine]; ok {
			return snap
		}
		snap, err := fetch(engine)
		if err != nil {
			snap = Snapshot{}
		}
		c.snaps[engine] = snap
		return snap
	}
}

// Poll fetches immediately, then every interval until ctx is done. Failures
// are recorded on the snapshot (Error + FetchedAt) rather than wedging, and
// deliberately keep being retried: an engine that is missing or logged out
// today may not be tomorrow, and "unavailable, because X" is exactly what an
// operator weighing a switch needs to see.
func (c *Cache) Poll(ctx context.Context, interval time.Duration, src Source) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		snap, err := Fetch(ctx, src)
		if err != nil {
			snap = Snapshot{Error: err.Error(), FetchedAt: time.Now()}
		}
		c.mu.Lock()
		c.snaps[src.Engine] = snap
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
