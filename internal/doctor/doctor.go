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
	engine := cfg.Engine()
	checks := []Check{
		binaryCheck(ctx, "gh", "gh", "--version", "install the GitHub CLI (brew install gh)"),
		authCheck(ctx, "gh-auth", "gh", []string{"auth", "status"}, "run `gh auth login`"),
		binaryCheck(ctx, "duckdb", store.DuckDBBin(), "--version", "install duckdb (brew install duckdb), or set AGENT_CODE_REVIEW_DUCKDB_PATH"),
	}
	checks = append(checks, engineChecks(ctx, engine, cfg)...)
	return append(checks, configCheck(review.Preflight(cfg.Review)))
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

// engineChecks probes the configured engine's CLI: present, and logged in.
// Only the configured engine is required, so a machine set up for one engine
// isn't told off for lacking the other. The switch stays because the two auth
// probes genuinely differ (codex reports via exit code, claude via JSON); only
// the binary lookup is shared, and that goes through the config getter.
func engineChecks(ctx context.Context, engine string, cfg config.Config) []Check {
	bin := cfg.EngineBin()
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
