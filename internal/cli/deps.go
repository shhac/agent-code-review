package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/discover"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/scheduler"
	"github.com/shhac/agent-code-review/internal/store"
)

// emit writes one JSON record to stdout (the family's NDJSON output contract).
func emit(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, string(b))
	return nil
}

// stderrLogf is the daemon/cycle log sink — human-readable, on stderr, so
// stdout stays clean for any NDJSON a command emits.
func stderrLogf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// buildScheduler wires the review engine, discoverer, and resolved gh user
// around an already-open store.
func buildScheduler(ctx context.Context, cfg config.Config, s store.Store) (*scheduler.Scheduler, error) {
	engine, err := review.NewEngine(cfg.Review)
	if err != nil {
		return nil, err
	}
	disc := discover.New(cfg, s, stderrLogf)

	ghUser := cfg.GHUser
	if ghUser == "" {
		if u, err := discover.CurrentUser(ctx); err == nil {
			ghUser = u
		} else {
			stderrLogf("warning: could not resolve gh user (%v); self-review rule will not fire", err)
		}
	}

	return scheduler.New(cfg, s, disc, engine, ghUser, stderrLogf), nil
}
