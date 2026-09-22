package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

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
	"github.com/shhac/agent-code-review/internal/usage"
)

// globals is the live flag snapshot, set once by newRootCmd so emit can honor
// -f/--format. Color is wired process-wide by libcli.NewRoot (--color →
// output.SetColorMode), so routing through EmitItem picks it up too.
var globals *libcli.Globals

// stdout is where records go; a variable so tests can read what a command
// printed.
var stdout io.Writer = os.Stdout

func format() string {
	if globals == nil {
		return ""
	}
	return globals.Format
}

// emit writes one record to stdout through the family output contract:
// NDJSON by default, -f json|yaml as the bare object, --color-aware.
func emit(v any) error {
	v, err := yamlReady(output.Format(format()), v)
	if err != nil {
		return err
	}
	return libcli.EmitItem(stdout, format(), v)
}

// emitList writes a list through the family list contract: NDJSON is one
// record per line, then one line per @-prefixed meta key; -f json|yaml is ONE
// document, {"data": [...], <meta keys>}. Emitting each item as a record of
// its own printed N concatenated documents under -f json, which no JSON
// parser reads as one, and N unseparated mappings under yaml. record maps an
// item (with its index) to the emitted shape; nil emits items as-is.
func emitList[T any](items []T, record func(int, T) any, meta map[string]any) error {
	f, err := output.ResolveFormat(format(), output.FormatNDJSON)
	if err != nil {
		return err
	}
	// Never nil, so an empty list still renders as "data": [] rather than null.
	records := make([]any, 0, len(items))
	for i, item := range items {
		v := any(item)
		if record != nil {
			v = record(i, item)
		}
		if v, err = yamlReady(f, v); err != nil {
			return err
		}
		records = append(records, v)
	}
	trailer := make(map[string]any, len(meta))
	for k, v := range meta {
		if trailer[k], err = yamlReady(f, v); err != nil {
			return err
		}
	}
	return output.WriteList(stdout, f, records, trailer, nil)
}

// yamlReady JSON-round-trips a value bound for the yaml encoder, so it uses
// the json-tag key names (yaml.v3 marshals Go structs by field name
// otherwise). The NDJSON/json paths marshal with the tags anyway and skip
// the extra encode.
func yamlReady(f output.Format, v any) (any, error) {
	if f != output.FormatYAML {
		return v, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var normalized any
	if err := json.Unmarshal(b, &normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

// logLine is one daemon log record. The timestamp is the point: a log full of
// "skipping repo this cycle" cannot answer when it started, how often it
// repeats, or whether it ever stopped, and that is exactly the question a
// stalled sweep raises. Level is the other: the lines worth grepping for are
// the ones where discovery gave up on a repo.
//
// msg stays a formatted sentence rather than named fields. Every call site is
// Printf-shaped today; giving the ones that earn it real fields is a later
// change this shape leaves room for.
type logLine struct {
	TS    string `json:"ts"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

// stderrLog writes the daemon's log to stderr as NDJSON, so stdout stays clean
// for any records a command emits and the daemon's own output obeys the same
// family contract (and colouring) as everything else.
var stderrLog = output.NewNDJSONWriter(os.Stderr)

func stderrLogAt(level, format string, args ...any) {
	_ = stderrLog.WriteItem(logLine{
		TS:    time.Now().UTC().Format(time.RFC3339),
		Level: level,
		Msg:   fmt.Sprintf(format, args...),
	})
}

// stderrLogf is the daemon/run log sink.
func stderrLogf(format string, args ...any) { stderrLogAt("info", format, args...) }

// stderrWarnf is its severity-carrying sibling, for the things somebody
// reading a log is actually looking for.
func stderrWarnf(format string, args ...any) { stderrLogAt("warn", format, args...) }

// logSinks is the severity-tagged log pair the scheduler and discoverer write
// through. One value rather than two parameters because the two are always
// chosen together: a one-shot run sends both straight to stderr, while the
// daemon tees both into the dashboard's ring, and splitting them has already
// meant every warning in the daemon arriving as an info line that happened to
// start with the word "warning".
type logSinks struct {
	infof func(string, ...any)
	warnf func(string, ...any)
}

// emitEach is emitList with no metadata: the shared frame behind every
// ls-style command.
func emitEach[T any](items []T, record func(int, T) any) error {
	return emitList(items, record, nil)
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

// fetchUsage is the real per-engine usage probe, as usage.Cache.Lazy wants it.
func fetchUsage(ctx context.Context, cfg config.Config) func(string) (usage.Snapshot, error) {
	return func(engine string) (usage.Snapshot, error) {
		return usage.Fetch(ctx, usage.Source{Engine: engine, Bin: cfg.BinFor(engine)})
	}
}

// resolveGHUser is the account reviews are posted as: gh_user when set, else
// whoever the gh CLI is logged in as. Failing to learn it is a warning rather
// than an error, because reviews still run; only the self-review rule goes
// quiet, so the warning has to say so.
func resolveGHUser(ctx context.Context, cfg config.Config, warnf func(notice, hint string)) string {
	if cfg.GHUser != "" {
		return cfg.GHUser
	}
	u, err := discover.CurrentUser(ctx)
	if err != nil {
		warnf(fmt.Sprintf("could not resolve gh user (%v); self-review rule will not fire", err),
			"set gh_user in config, or authenticate the gh CLI")
		return ""
	}
	return u
}

// buildScheduler wires the discoverer and resolved gh user around an
// already-open store. Config flows through the getter so cadence, dials, and
// codex settings reload live (the engine itself is rebuilt per review); the
// engine name is validated up front so a typo still fails at boot. logf is
// the scheduler log sink: plain stderr for one-shot runs; serve tees it into the
// dashboard's log ring. warnf carries agent-actionable warnings: one-shot
// runs route it to output.WriteNotice so stderr stays structured; serve folds
// it into the daemon log (and thus the dashboard's log ring). usageFn feeds
// the usage-floor hold; nil bypasses the floor entirely, which only
// `run --ignore-usage-floor` asks for.
func buildScheduler(ctx context.Context, cfgFn func() config.Config, s store.Store, logs logSinks, warnf func(notice, hint string), usageFn scheduler.UsageFn) (*scheduler.Scheduler, error) {
	cfg := cfgFn()
	// Every engine a group or override can route to, not just the configured
	// one: an engine is now chosen per candidate, so a typo in a rarely-used
	// group must fail at boot rather than at 3am on the one PR that hits it.
	for _, engine := range cfg.ReachableEngines() {
		if _, err := review.NewEngine(cfg.Review.WithPolicy(config.Policy{Engine: engine})); err != nil {
			return nil, err
		}
	}
	ghUser := resolveGHUser(ctx, cfg, warnf)
	disc := discover.New(cfgFn, s, logs.infof).WithWarnf(logs.warnf).WithSelfLogin(ghUser)

	// Pricing is read from the cache dir, never fetched here: `run`
	// and the daemon both value reviews from whatever the last refresh left on
	// disk, and neither waits on the network to record an outcome. An empty
	// cache simply records no estimate.
	prices := pricing.Open(config.PricingCacheDir())
	return scheduler.New(scheduler.Deps{
		Store:   s,
		Config:  cfgFn,
		Sweeper: disc,
		GHUser:  ghUser,
		Logf:    logs.infof,
		Usage:   usageFn,
		Price:   estimator(prices),
	}), nil
}
