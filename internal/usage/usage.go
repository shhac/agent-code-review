// Package usage reads Codex rate-limit status the same way the desktop app
// does: spawn `codex app-server`, speak JSON-RPC over stdio (initialize →
// account/rateLimits/read), and parse the snapshot. A Cache polls on an
// interval so the dashboard can show usage without a subprocess per request.
package usage

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
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

// wire shapes for the app-server protocol (only the fields we read).
type rpcWindow struct {
	UsedPercent float64 `json:"usedPercent"`
	WindowMins  int     `json:"windowDurationMins"`
	ResetsAt    int64   `json:"resetsAt"`
}

type rpcResponse struct {
	ID     int `json:"id"`
	Result struct {
		RateLimits struct {
			PlanType  string     `json:"planType"`
			Primary   *rpcWindow `json:"primary"`
			Secondary *rpcWindow `json:"secondary"`
		} `json:"rateLimits"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Fetch spawns the codex app-server, requests the account rate limits, and
// tears the process down. bin is the codex binary ("codex" when empty).
func Fetch(ctx context.Context, bin string) (Snapshot, error) {
	if bin == "" {
		bin = "codex"
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "app-server")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Snapshot{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Snapshot{}, err
	}
	if err := cmd.Start(); err != nil {
		return Snapshot{}, err
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	if err := writeHandshake(stdin); err != nil {
		return Snapshot{}, err
	}

	return parseRateLimits(stdout)
}

// rateLimitsRequestID identifies the rateLimits/read call in the handshake;
// parseRateLimits scans the response stream for exactly this id.
const rateLimitsRequestID = 2

// handshakeMessages is the request half of the app-server protocol:
// initialize, the initialized notification, then the rateLimits read. Pure,
// so the wire contract is pinned by table tests — parseRateLimits covers the
// response half the same way.
func handshakeMessages() []map[string]any {
	return []map[string]any{
		{
			"jsonrpc": "2.0", "id": 1, "method": "initialize",
			"params": map[string]any{"clientInfo": map[string]any{
				"name": "agent-code-review", "title": "agent-code-review", "version": "dev",
			}},
		},
		{"jsonrpc": "2.0", "method": "initialized"},
		{"jsonrpc": "2.0", "id": rateLimitsRequestID, "method": "account/rateLimits/read", "params": map[string]any{}},
	}
}

// writeHandshake frames the handshake messages onto the app-server's stdin:
// one JSON object per newline-terminated line.
func writeHandshake(w io.Writer) error {
	for _, msg := range handshakeMessages() {
		b, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// parseRateLimits scans the app-server's stdout stream for the rateLimits response
// and maps it to a Snapshot. Pure over an io.Reader: the skip/error/mapping
// branches are tested from canned streams without spawning codex.
func parseRateLimits(r io.Reader) (Snapshot, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		var resp rpcResponse
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil || resp.ID != rateLimitsRequestID {
			continue
		}
		if resp.Error != nil {
			return Snapshot{}, fmt.Errorf("codex app-server: %s", resp.Error.Message)
		}
		rl := resp.Result.RateLimits
		return Snapshot{
			Plan:      rl.PlanType,
			Primary:   toWindow(rl.Primary),
			Secondary: toWindow(rl.Secondary),
			FetchedAt: time.Now(),
		}, nil
	}
	if err := scanner.Err(); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{}, fmt.Errorf("codex app-server closed without a rate-limit response")
}

func toWindow(w *rpcWindow) *Window {
	if w == nil {
		return nil
	}
	return &Window{UsedPercent: w.UsedPercent, WindowMins: w.WindowMins, ResetsAt: w.ResetsAt}
}

// Cache holds the latest snapshot and refreshes it on an interval.
type Cache struct {
	mu   sync.RWMutex
	snap Snapshot
}

func NewCache() *Cache { return &Cache{} }

// Get returns the latest snapshot (zero value until the first poll lands).
func (c *Cache) Get() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snap
}

// Poll fetches immediately, then every interval until ctx is done. Failures
// are recorded on the snapshot (Error + FetchedAt) rather than wedging.
func (c *Cache) Poll(ctx context.Context, interval time.Duration, bin string) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		snap, err := Fetch(ctx, bin)
		if err != nil {
			snap = Snapshot{Error: err.Error(), FetchedAt: time.Now()}
		}
		c.mu.Lock()
		c.snap = snap
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
