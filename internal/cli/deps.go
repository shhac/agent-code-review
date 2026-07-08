package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	libcli "github.com/shhac/lib-agent-cli/cli"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/discover"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/scheduler"
	"github.com/shhac/agent-code-review/internal/store"
)

// globals is the live flag snapshot, set once by newRootCmd so emit can honor
// -f/--format. Color is wired process-wide by libcli.NewRoot (--color →
// output.SetColorMode), so routing through EmitItem picks it up too.
var globals *libcli.Globals

// emit writes one record to stdout through the family output contract:
// NDJSON by default, -f json|yaml envelopes, --color-aware. Values are
// JSON-round-tripped first so every format uses the json-tag key names
// (the yaml encoder marshals Go structs by field name otherwise).
func emit(v any) error {
	format := ""
	if globals != nil {
		format = globals.Format
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var normalized any
	if err := json.Unmarshal(b, &normalized); err != nil {
		return err
	}
	return libcli.EmitItem(os.Stdout, format, normalized)
}

// stderrLogf is the daemon/cycle log sink — human-readable, on stderr, so
// stdout stays clean for any NDJSON a command emits.
func stderrLogf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// buildScheduler wires the discoverer and resolved gh user around an
// already-open store. Config flows through the getter so cadence, dials, and
// codex settings reload live (the engine itself is rebuilt each cycle); the
// engine name is validated up front so a typo still fails at boot. logf is
// the cycle log sink: plain stderr for one-shot runs; serve tees it into the
// dashboard's log ring. usageFn feeds the usage-floor pause; nil (one-shot
// runs) bypasses the floor.
func buildScheduler(ctx context.Context, cfgFn func() config.Config, s store.Store, logf func(string, ...any), usageFn scheduler.UsageFn) (*scheduler.Scheduler, error) {
	cfg := cfgFn()
	if _, err := review.NewEngine(cfg.Review); err != nil {
		return nil, err
	}
	disc := discover.New(cfgFn, s, logf)

	ghUser := cfg.GHUser
	if ghUser == "" {
		if u, err := discover.CurrentUser(ctx); err == nil {
			ghUser = u
		} else {
			logf("warning: could not resolve gh user (%v); self-review rule will not fire", err)
		}
	}

	return scheduler.New(cfgFn, s, disc, ghUser, logf, usageFn), nil
}
