// Package doctor diagnoses whether this machine can actually run a review.
//
// It exists because every dependency here fails LATE and quietly: a missing
// or logged-out engine CLI surfaces only as repeated ERROR history rows with
// the reason buried in the engine transcript, and a model that the permission
// classifier rejects fails identically on every PR. Reading a queue full of
// ERRORs tells you something is wrong but not what. These checks answer that
// in one command, and the daemon runs them at boot so the answer is in the
// log before the first failed review rather than after the twentieth.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/pricing"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
	"github.com/shhac/agent-code-review/internal/usage"
)

// probeTimeout bounds each external command. Generous enough for a cold
// binary, short enough that a wedged CLI cannot hang boot.
const probeTimeout = 15 * time.Second

// Check is one diagnosis. Blocking marks a failure that stops reviews working
// at all, as opposed to one that only degrades something (a usage meter, a
// shell completion).
type Check struct {
	Name     string `json:"check"`
	OK       bool   `json:"ok"`
	Blocking bool   `json:"blocking"`
	Detail   string `json:"detail,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

// Run executes every check against the given config. Never returns an error:
// a failed check IS the result, and the caller decides what a failure means.
func Run(ctx context.Context, cfg config.Config) []Check {
	checks := []Check{
		binaryCheck(ctx, "gh", "gh", "--version", "install the GitHub CLI (brew install gh)"),
		authCheck(ctx, "gh-auth", "gh", []string{"auth", "status"}, "run `gh auth login`"),
		binaryCheck(ctx, "duckdb", store.DuckDBBin(), "--version", "install duckdb (brew install duckdb), or set AGENT_CODE_REVIEW_DUCKDB_PATH"),
	}
	// Every engine any author group can route to, not just the configured
	// one: a typo in a rarely-used group would otherwise surface at 3am as an
	// ERROR row. Not every WIRED engine either, which would fail a deploy over
	// an engine nothing references.
	for _, engine := range cfg.ReachableEngines() {
		checks = append(checks, engineChecks(ctx, engine, cfg)...)
	}
	checks = append(checks, pricingCheck(config.PricingCacheDir()))
	return append(checks, configCheck(ConfigProblems(cfg)))
}

// ConfigProblems is every statically detectable misconfiguration: the author
// groups themselves, plus each engine-settings combination a group can
// actually produce. Preflight's checks are model-and-mode specific (the
// auto-mode model trap), and a group naming its own model is exactly what can
// introduce a bad pairing that the base config does not have.
//
// Exported so boot validation reports the same problems `doctor` does.
func ConfigProblems(cfg config.Config) []string {
	problems := cfg.ValidateAuthors()
	for _, rs := range reachableSettings(cfg) {
		for _, p := range review.Preflight(rs.settings) {
			problems = append(problems, rs.where+p)
		}
	}
	return problems
}

// reachableSettings enumerates the distinct engine configurations a review can
// actually run under: the base settings, then each group's and each override's
// patch of them. Deduplicated on the fields Preflight judges, so a dozen
// groups sharing one model report one problem rather than a dozen; `where`
// attributes each to whoever introduced it.
func reachableSettings(cfg config.Config) []struct {
	where    string
	settings config.ReviewSettings
} {
	type entry = struct {
		where    string
		settings config.ReviewSettings
	}
	seen := map[string]bool{}
	var out []entry
	add := func(where string, policy config.Policy) {
		settings := cfg.Review.WithPolicy(policy)
		key := fmt.Sprintf("%s|%s|%s|%s|%s|%s",
			settings.Engine, settings.Claude.Model, settings.Claude.Effort,
			settings.Claude.PermissionMode, settings.Codex.Model, settings.Codex.Effort)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, entry{where: where, settings: settings})
	}
	add("", config.Policy{})
	for _, name := range cfg.GroupNames() {
		if g, ok := cfg.Group(name); ok {
			add("group "+name+": ", config.Policy{Engine: g.Engine, Model: g.Model, Effort: g.Effort})
		}
	}
	for _, o := range cfg.Authors.Overrides {
		add("override "+o.Handle+": ", config.Policy{Engine: o.Engine, Model: o.Model, Effort: o.Effort})
	}
	return out
}

// pricingCheck reports the model price table. Never blocking: only claude
// values its own runs, so the table is what lets codex spend be estimated at
// all, but a review runs identically without it. The daemon refreshes it in
// the background, so an absent table on a machine that has never run `serve`
// is expected rather than a fault.
func pricingCheck(dir string) Check {
	st := pricing.Open(dir).Status()
	if st.Models == 0 {
		return Check{Name: "pricing", OK: false, Blocking: false,
			Detail: "no model price table cached",
			Hint:   "run `agent-code-review serve` once; it fetches and refreshes it every " + pricing.RefreshInterval.String()}
	}
	age := time.Since(st.FetchedAt).Truncate(time.Minute)
	return Check{Name: "pricing", OK: true, Blocking: false,
		Detail: fmt.Sprintf("%d models, checked %s ago", st.Models, age)}
}

// configCheck folds every static configuration problem into ONE check.
// Check.Name is a unique key everywhere else here — the CLI keys its exit
// message off the first failure, and any consumer reading the NDJSON rows
// reasonably assumes one row per check. Emitting a row per problem broke that
// silently: a config with two problems reported two rows both called
// engine-config, and only the first reached the error message.
func configCheck(problems []string) Check {
	if len(problems) == 0 {
		return Check{Name: "engine-config", OK: true, Blocking: true, Detail: "no conflicting settings"}
	}
	return Check{
		Name: "engine-config", OK: false, Blocking: true,
		Detail: strings.Join(problems, "; "),
		Hint:   "agent-code-review config set ...",
	}
}

// Blocking reports whether any blocking check failed, i.e. whether reviews
// would fail on this machine right now.
func Blocking(checks []Check) []Check {
	var failed []Check
	for _, c := range checks {
		if !c.OK && c.Blocking {
			failed = append(failed, c)
		}
	}
	return failed
}

// engineChecks probes one engine's CLI: present, and logged in. Only engines
// a group can actually route to are probed, so a machine set up for one engine
// isn't told off for lacking the other. The switch stays because the two auth
// probes genuinely differ (codex reports via exit code, claude via JSON); only
// the binary lookup is shared, and that goes through the config getter.
func engineChecks(ctx context.Context, engine string, cfg config.Config) []Check {
	bin := cfg.BinFor(engine)
	switch engine {
	case "claude":
		if bin == "" {
			bin = "claude"
		}
		return []Check{
			binaryCheck(ctx, "engine:claude", bin, "--version", "install Claude Code, or set claude.bin"),
			claudeAuthCheck(ctx, bin),
		}
	default:
		if bin == "" {
			bin = "codex"
		}
		return []Check{
			binaryCheck(ctx, "engine:codex", bin, "--version", "install the Codex CLI, or set codex.bin"),
			authCheck(ctx, "engine:codex-auth", bin, []string{"login", "status"}, "run `codex login`"),
		}
	}
}

// binaryCheck resolves the binary and reads its version, so the detail line
// records exactly which build is in play.
func binaryCheck(ctx context.Context, name, bin, versionArg, hint string) Check {
	if _, err := exec.LookPath(bin); err != nil {
		return Check{Name: name, Blocking: true, Detail: fmt.Sprintf("%q not on PATH", bin), Hint: hint}
	}
	out, err := run(ctx, bin, versionArg)
	if err != nil {
		return Check{Name: name, Blocking: true, Detail: fmt.Sprintf("%q found but %s failed: %v", bin, versionArg, err), Hint: hint}
	}
	return Check{Name: name, OK: true, Blocking: true, Detail: firstLine(out)}
}

// authCheck treats a zero exit as authenticated, which is the contract both
// `gh auth status` and `codex login status` follow.
func authCheck(ctx context.Context, name, bin string, args []string, hint string) Check {
	if _, err := exec.LookPath(bin); err != nil {
		return Check{Name: name, Blocking: true, Detail: fmt.Sprintf("%q not on PATH", bin), Hint: hint}
	}
	out, err := run(ctx, bin, args...)
	if err != nil {
		return Check{Name: name, Blocking: true, Detail: "not authenticated", Hint: hint}
	}
	return Check{Name: name, OK: true, Blocking: true, Detail: firstLine(out)}
}

// claudeAuthCheck reads the structured status rather than the exit code:
// `claude auth status` exits 0 while logged out and reports it in the JSON.
func claudeAuthCheck(ctx context.Context, bin string) Check {
	const hint = "run `claude auth login`"
	if _, err := exec.LookPath(bin); err != nil {
		return Check{Name: "engine:claude-auth", Blocking: true, Detail: fmt.Sprintf("%q not on PATH", bin), Hint: hint}
	}
	status, err := usage.ReadClaudeAuthStatus(ctx, bin)
	if err != nil {
		detail := "auth status failed"
		if errors.Is(err, usage.ErrClaudeAuthUnreadable) {
			detail = "auth status was not readable JSON"
		}
		return Check{Name: "engine:claude-auth", Blocking: true, Detail: detail, Hint: hint}
	}
	if !status.LoggedIn {
		return Check{Name: "engine:claude-auth", Blocking: true, Detail: "not logged in", Hint: hint}
	}
	return Check{Name: "engine:claude-auth", OK: true, Blocking: true,
		Detail: strings.TrimSpace(fmt.Sprintf("%s %s", status.AuthMethod, status.SubscriptionType))}
}

func run(ctx context.Context, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	return string(out), err
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
