package usage

// The codex headroom reader: spawn `codex app-server`, speak JSON-RPC over
// stdio the way the desktop app does, and map the rate-limit snapshot onto the
// engine-agnostic Snapshot in usage.go. Claude's reader is the sibling
// claude.go; usage.go holds only what both share.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"time"
)

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

// fetchCodex spawns the codex app-server, requests the account rate limits,
// and tears the process down. bin is the codex binary ("codex" when empty).
func fetchCodex(ctx context.Context, bin string) (Snapshot, error) {
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
