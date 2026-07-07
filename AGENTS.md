# agent-code-review

PR review queue + scheduler for AI agents. Go + cobra on the `lib-agent-*`
family libraries, compiled to a standalone (CGO-free) binary.

## Architecture

```
cmd/agent-code-review/main.go   # entry point; version injected via -ldflags
internal/
├── cli/
│   ├── root.go                 # lib-agent-cli NewRoot; registers subcommands
│   ├── deps.go                 # buildScheduler (engine + discoverer + gh user); emit()
│   ├── serve.go                # `serve` daemon: scheduler + dashboard + tailscale.Wire
│   ├── run.go                  # `run --once`: single review cycle
│   ├── queue.go                # `queue ls/add/rm/promote/skip`
│   ├── authors.go              # `authors allow/deny/ls` — whose PRs we may approve
│   ├── configcmd.go            # `config path/show`
│   └── usage.go                # top-level LLM reference card
├── config/                     # ~/.config/agent-code-review/config.json + resolved defaults
├── store/                      # Store interface + DuckDB subprocess driver + schema.sql
├── discover/                   # gh pr list → New/Refreshed classification
├── review/                     # Engine interface + codex driver + prompt/rule assembly
├── scheduler/                  # run-lock, ordering, parallelism cap, cycle orchestration
└── dashboard/                  # embedded web UI + JSON API over the store
```

## Key patterns

- **Family libraries**: `lib-agent-cli` (root scaffolding, XDG paths, creds
  store), `lib-agent-output` (NDJSON contract, `{error, fixable_by, hint}`),
  `lib-agent-mcp/tailscale` (the `--tailscale serve|funnel` wiring). Prefer these
  over hand-rolling; `agent-sql`, `agent-mongo`, and `agent-mcp-host` are the
  sibling references.
- **Go owns the deterministic machinery; the engine owns everything fuzzy.** The
  scheduler/store/discovery are testable Go. The review itself, comment-only
  enforcement, and Slack steps are expressed as **prompt** (config `review.rules`)
  handed to Codex — never as Go control flow. See `design-docs/2026-07-architecture.md`.
- **DuckDB via subprocess.** CGO-free so the binary cross-compiles through the
  family release pipeline. Mirrors `agent-sql`'s driver. Requires the `duckdb`
  CLI at runtime.
- **Nothing environment-specific in code.** Repos, prompts, and cadence are
  config; the allowed-authors list (whose PRs we may approve) is per-repo
  runtime data in the store (managed via `authors`). Never hardcode a GitHub
  handle or repo — not in code, docs, or the example config.

## Conventions

- `const`/early-return, avoid `as`-style casts (see `CLAUDE.local.md`).
- Tests colocated as `_test.go`. `make test` runs everything; discovery,
  prompt/rules, and config defaults are unit-tested without external deps.
- Errors: `output.New(msg, output.FixableByAgent|Human|Retry)`.
