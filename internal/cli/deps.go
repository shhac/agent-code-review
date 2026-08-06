package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	libcli "github.com/shhac/lib-agent-cli/cli"
	output "github.com/shhac/lib-agent-output"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/discover"
	"github.com/shhac/agent-code-review/internal/doctor"
	"github.com/shhac/agent-code-review/internal/pricing"
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
// NDJSON by default, -f json|yaml envelopes, --color-aware. Only the yaml
// path JSON-round-trips the value first, so its encoder uses the json-tag
// key names (yaml.v3 marshals Go structs by field name otherwise); the
// NDJSON/json paths marshal with the tags anyway and skip the extra encode.
func emit(v any) error {
	format := ""
	if globals != nil {
		format = globals.Format
	}
	if output.Format(format) == output.FormatYAML {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		var normalized any
		if err := json.Unmarshal(b, &normalized); err != nil {
			return err
		}
		v = normalized
	}
	return libcli.EmitItem(os.Stdout, format, v)
}

// stderrLogf is the daemon/cycle log sink: human-readable, on stderr, so
// stdout stays clean for any NDJSON a command emits.
func stderrLogf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// emitEach emits one record per item, stopping at the first write error:
// the shared frame behind every ls-style command. record maps an item (with
// its index) to the emitted shape; nil emits items as-is.
func emitEach[T any](items []T, record func(int, T) any) error {
	for i, item := range items {
		v := any(item)
		if record != nil {
			v = record(i, item)
		}
		if err := emit(v); err != nil {
			return err
		}
	}
	return nil
}

// filterFold removes every element whose name matches target
// (case-insensitive), returning the kept slice and how many were removed:
// the shared frame behind the case-insensitive rm commands.
func filterFold[T any](list []T, name func(T) string, target string) ([]T, int) {
	kept := list[:0]
	removed := 0
	for _, item := range list {
		if strings.EqualFold(name(item), target) {
			removed++
			continue
		}
		kept = append(kept, item)
	}
	return kept, removed
}

// self is filterFold's name function for plain string lists.
func self(s string) string { return s }

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
		return "", 0, invalidRepo(args[0])
	case err != nil:
		return "", 0, output.New("PR number must be an integer, got "+args[1], output.FixableByAgent)
	}
	return ref.Repo, ref.Number, nil
}

// unknownGroup is the shared "no such cohort" error. The resolver deliberately
// treats an unknown group as comment-only rather than failing a review, which
// is right at review time and wrong at write time: here it is a typo someone is
// still looking at, so every command that accepts a group name rejects it the
// same way, listing the same valid set. hint adds the per-command nudge (which
// flag, or where to define one) and may be empty.
func unknownGroup(cfg config.Config, name, hint string) error {
	msg := "Unknown group " + name + ". Valid: " + strings.Join(cfg.GroupNames(), ", ")
	if hint != "" {
		msg += ". " + hint
	}
	return output.New(msg, output.FixableByAgent)
}

// invalidEnum is the shared enum-flag error: one wording for every
// "--flag must be one of ..." failure, built from the same slice the
// completions offer.
func invalidEnum(flag string, valid []string, got string) error {
	return output.New(flag+" must be one of "+strings.Join(valid, ", ")+", got "+got, output.FixableByAgent)
}

// prKey renders the canonical "owner/repo#N" reference used in emit keys and
// error messages.
func prKey(repo string, number int) string {
	return prref.Ref{Repo: repo, Number: number}.String()
}

// reportConfigProblems surfaces every statically detectable misconfiguration
// through the caller's warning channel.
//
// A preflight step callers invoke, not something buildScheduler does on the
// way past. Emitting diagnostics from a constructor made "build a scheduler"
// mean "build a scheduler and also run half of doctor", which serve then did
// twice: once here and once through its own doctor.Run at boot.
func reportConfigProblems(cfg config.Config, warnf func(notice, hint string)) {
	for _, problem := range doctor.ConfigProblems(cfg) {
		warnf(problem, "agent-code-review doctor")
	}
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
	// Every engine a group or override can route to, not just the configured
	// one: an engine is now chosen per candidate, so a typo in a rarely-used
	// group must fail at boot rather than at 3am on the one PR that hits it.
	for _, engine := range cfg.ReachableEngines() {
		if _, err := review.NewEngine(cfg.Review.WithPolicy(config.Policy{Engine: engine})); err != nil {
			return nil, err
		}
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

	// Pricing is read from the cache dir, never fetched here: `run --once`
	// and the daemon both value reviews from whatever the last refresh left on
	// disk, and neither waits on the network to record an outcome. An empty
	// cache simply records no estimate.
	prices := pricing.Open(config.PricingCacheDir())
	return scheduler.New(cfgFn, s, disc, ghUser, logf, usageFn).WithPricing(estimator(prices)), nil
}

// estimator adapts the price table to the scheduler's PriceFn. A model the
// table does not list, or a review with no class split, yields false: the row
// records no estimate rather than a zero that would read as a free review.
func estimator(prices *pricing.Cache) scheduler.PriceFn {
	return func(model string, input, output, cacheWrite, cacheRead int) (float64, bool) {
		if input+output == 0 {
			return 0, false
		}
		rates, ok := prices.Lookup(model)
		if !ok {
			return 0, false
		}
		return rates.Cost(input, output, cacheWrite, cacheRead), true
	}
}
