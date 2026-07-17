package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	libcli "github.com/shhac/lib-agent-cli/cli"
	output "github.com/shhac/lib-agent-output"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/discover"
	"github.com/shhac/agent-code-review/internal/prref"
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

// stderrLogf is the daemon/cycle log sink: human-readable, on stderr, so
// stdout stays clean for any NDJSON a command emits.
func stderrLogf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// withStore opens the store, runs fn, and closes it: the session helper
// every store-touching command wraps its RunE in.
func withStore(fn func(store.Store) error) error {
	s, err := openStore(config.Read())
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	return fn(s)
}

// parseRepoNumber maps the canonical <owner/repo> <number> positional pair
// onto the CLI's error envelope.
func parseRepoNumber(args []string) (string, int, error) {
	ref, err := prref.Parse(args[0], args[1])
	switch {
	case errors.Is(err, prref.ErrRepo):
		return "", 0, output.New("Repo must be owner/name, got "+args[0], output.FixableByAgent)
	case err != nil:
		return "", 0, output.New("PR number must be an integer, got "+args[1], output.FixableByAgent)
	}
	return ref.Repo, ref.Number, nil
}

// prKey renders the canonical "owner/repo#N" reference used in emit keys and
// error messages.
func prKey(repo string, number int) string {
	return prref.Ref{Repo: repo, Number: number}.String()
}

// buildScheduler wires the discoverer and resolved gh user around an
// already-open store. Config flows through the getter so cadence, dials, and
// codex settings reload live (the engine itself is rebuilt each cycle); the
// engine name is validated up front so a typo still fails at boot. logf is
// the cycle log sink: plain stderr for one-shot runs; serve tees it into the
// dashboard's log ring. warnf carries agent-actionable warnings: one-shot
// runs route it to output.WriteNotice so stderr stays structured; serve folds
// it into the daemon log (and thus the dashboard's log ring). usageFn feeds
// the usage-floor pause; nil (one-shot runs) bypasses the floor.
func buildScheduler(ctx context.Context, cfgFn func() config.Config, s store.Store, logf func(string, ...any), warnf func(notice, hint string), usageFn scheduler.UsageFn) (*scheduler.Scheduler, error) {
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
			warnf(fmt.Sprintf("could not resolve gh user (%v); self-review rule will not fire", err),
				"set gh_user in config, or authenticate the gh CLI")
		}
	}

	return scheduler.New(cfgFn, s, disc, ghUser, logf, usageFn), nil
}
