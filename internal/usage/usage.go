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

	send := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = stdin.Write(append(b, '\n'))
		return err
	}
	if err := send(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"clientInfo": map[string]any{
			"name": "agent-code-review", "title": "agent-code-review", "version": "dev",
		}},
	}); err != nil {
		return Snapshot{}, err
	}
	if err := send(map[string]any{"jsonrpc": "2.0", "method": "initialized"}); err != nil {
		return Snapshot{}, err
	}
	if err := send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "account/rateLimits/read", "params": map[string]any{}}); err != nil {
		return Snapshot{}, err
	}

	return parseRateLimits(stdout)
}

// parseRateLimits scans the app-server's stdout stream for the id=2 response
// and maps it to a Snapshot. Pure over an io.Reader — the skip/error/mapping
// branches are tested from canned streams without spawning codex.
func parseRateLimits(r io.Reader) (Snapshot, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		var resp rpcResponse
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil || resp.ID != 2 {
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
