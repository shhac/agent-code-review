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
│   ├── queue.go                # `queue ls/add/rm/promote/skip/log`
│   ├── authors.go              # `authors allow/deny/ls`: whose PRs we may approve
│   ├── repos.go                # `repos ls/add/rm`: the watched repos (config)
│   ├── prompts.go              # `prompts show/set/unset/preview`: review prompts
│   ├── configcmd.go            # `config init/path/show/list/get/set/unset`
│   └── usage.go                # top-level LLM reference card
├── config/                     # ~/.config/agent-code-review/config.json + resolved defaults
├── store/                      # Store interface + DuckDB subprocess driver + schema.sql
├── discover/                   # gh pr list → New/Refreshed classification
├── review/                     # Engine interface + codex driver + prompt/rule assembly
├── scheduler/                  # run-lock, heartbeat loops, parallelism cap, cycle orchestration
├── usage/                      # codex app-server rate-limit polling + usage-floor predicate
├── logbuf/                     # in-memory ring for the daemon's own log tail
└── dashboard/                  # embedded web UI + JSON API over the store
    ├── dashboard.go            # server core + thin read handlers
    ├── queue.go                # queue write surface (add/reorder/remove) + statuses
    ├── reviewlog.go            # /api/review-log: live/postmortem agent-log tail
    ├── stats.go                # /api/stats: last-24h outcome buckets
    ├── ui/                     # Svelte + Vite source (npm; not embedded)
    └── assets/                 # BUILT bundle, committed + go:embed'd
```

## Key patterns

- **The dashboard bundle is committed, not built in CI.** `make dashboard`
  (npm run build in `internal/dashboard/ui`) writes into
  `internal/dashboard/assets/`, which `go:embed` ships and the release
  workflow embeds as-is via `go build`. After ANY change under `ui/src`,
  run `make dashboard` and commit the regenerated assets; CI's
  `dashboard-fresh` job rebuilds and diffs to enforce this. Release ritual:
  `make release VERSION=vX.Y.Z` (verifies tag availability, clean tree,
  dashboard freshness, Go tests, `go vet`, and frontend tests), then `git tag
  vX.Y.Z` and `git push origin main vX.Y.Z`. Pushing the `v*` tag is the only
  build trigger: the Release workflow (`.github/workflows/release.yml`)
  cross-builds the CGO-free binaries, publishes the GitHub Release, and updates
  the Homebrew formula. You never cross-compile or upload artifacts by hand;
  locally you only commit the dashboard bundle and push the tag.

- **Family libraries**: `lib-agent-cli` (root scaffolding, XDG paths, creds
  store), `lib-agent-output` (NDJSON contract, `{error, fixable_by, hint}`),
  `lib-agent-mcp/tailscale` (the `--tailscale serve|funnel` wiring). Prefer these
  over hand-rolling; `agent-sql`, `agent-mongo`, and `agent-mcp-host` are the
  sibling references.
- **Go owns the deterministic machinery; the engine owns everything fuzzy.** The
  scheduler/store/discovery are testable Go. The review itself and all
  post-outcome behaviour are expressed as **prompt** (config `review.main_prompt`,
  `on_approve`/`on_comment`/`on_reject`, `review.rules`) handed to the engine,
  never as Go control flow. The tool assumes only the gh + codex CLIs; skills
  and extra CLIs are user-prompt territory. See `design-docs/2026-07-architecture.md`.
- **DuckDB via subprocess.** CGO-free so the binary cross-compiles through the
  family release pipeline. Mirrors `agent-sql`'s driver. Requires the `duckdb`
  CLI at runtime.
- **Config reloads live via getters.** Scheduler, discoverer, and dashboard
  hold `func() config.Config` and re-read per cycle/sweep/request (each
  operation snapshots ONCE and threads the snapshot). The loop on/off
  switches are NOT config: serve resolves config defaults + `--no-*` flags
  once at boot and passes them to `StartGraceful` as explicit parameters, so
  a config edit can't resurrect a loop this boot disabled.
- **Queue row ⇔ pending work.** Completion moves a candidate into append-only
  history atomically (SHA-gated `Complete`); "reviewing" is derived from a
  claim lease (`ClaimActive`, window `LeaseWindow()`), never stored as a
  status column. Likewise "held" is derived from the eligibility hold
  (`Held`, `eligible_at`/`hold_reason`): discovered candidates wait out a
  quiet period (PR updated too recently) and a re-review cooldown (we
  reviewed it too recently) while sitting visibly in the queue. Holds only
  ever extend on re-sweep; `Promote` (= review now) clears the hold, floats
  the row, and escalates to manual; drag-reorder never touches holds or
  source. Queue order is FIFO by first discovery (`discovered_at` is
  first-seen, never bumped). Idle review cycles exit before the run-lock;
  the 1m default cadence records nothing while the queue is empty or held.
- **Nothing environment-specific in code.** Repos, prompts, and cadence are
  config; the allowed-authors list (whose PRs we may approve) is per-repo
  runtime data in the store (managed via `authors`). Never hardcode a GitHub
  handle or repo, not in code, docs, or the example config.

- **Crash/concurrency safety.** Claims are compare-and-swap leases carrying
  host+pid (`Store.Claim` returns whether you won; losing is a clean skip),
  and boot runs `Scheduler.Reconcile` to release run rows and claims left by
  a dead pid on this host, so a mid-review crash never blocks the next boot
  for the lease window. `serve` binds the dashboard port before starting any
  loop, so a second instance on the same address exits before it can claim
  or review anything.

## Conventions

- **Dev boots: never point a second live _read-write_ instance at the real
  store.** A write-open fights the daemon for the DuckDB file and a review loop
  claims real PRs / spends real tokens. Pick the launch that matches what
  you're testing; no rediscovery needed:
  - **Inspect real data safely** (charts, history, the built/embedded dashboard
    against production data): `make dev ARGS="serve --read-only --http
    127.0.0.1:8399"`. Opens the store read-only (safe _alongside_ the running
    daemon because DuckDB here is subprocess-per-statement), forces both loops
    off, and lets the DB refuse any write. A non-default `--http` port is
    needed since the daemon already holds `:8330`.
  - **Iterate on the frontend** (hot reload, no rebuild): `cd
    internal/dashboard/ui && npm run dev`. Vite serves `ui/src` and proxies
    `/api` to a running daemon (default `127.0.0.1:8330`; target another with
    the `ACR_API` env var, e.g. `ACR_API=http://127.0.0.1:9000 npm run dev`).
    Best loop for UI work: real data, instant reload.
  - **Exercise a loop**: `serve --no-schedule` (dashboard only), then opt into
    `--no-reviews` (discovery only) or a scratch store (`XDG_CONFIG_HOME`/
    `XDG_DATA_HOME` to a temp dir, or `store.path` in a scratch config) before
    enabling reviews.

- `const`/early-return, avoid `as`-style casts (see `CLAUDE.local.md`).
- Tests colocated as `_test.go`. `make test` runs everything; discovery,
  prompt/rules, and config defaults are unit-tested without external deps.
  `make test-integration` adds the DuckDB round-trips and (env-gated) live
  codex/gh paths.
- **Test via injection, not subprocesses.** Extract pure cores and table-test
  them; for effectful code, fake the narrow dependency (embed `store.Store`
  in a struct that overrides only the methods under test, so an unexpected
  call panics loudly). Scheduler tests inject the engine via `newEngine` and
  the recheck via `stillCandidate`; discovery fakes its four-method
  `candidateStore`.
- Errors: `output.New(msg, output.FixableByAgent|Human|Retry)`.
