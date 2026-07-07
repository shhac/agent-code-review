# Architecture & scaffold decisions

Decision date: 2026-07-07. Pins: Go 1.26; lib-agent-cli v0.19.0; lib-agent-mcp
v0.23.1 (for the `tailscale` subpackage); lib-agent-output v0.10.0.

## What this is

`agent-code-review` turns a Codex scheduled prompt — "act as a friendly PR
unblocker across our repos" — into a CLI in the `agent-*` family. It discovers
candidate PRs, keeps a DuckDB-backed review queue, reviews each candidate by
handing an assembled prompt to a pluggable engine, and serves a dashboard that
can be exposed over Tailscale.

## The load-bearing decision: Go owns the deterministic machinery; the engine owns everything fuzzy

The Go binary is a **queue manager + scheduler + dashboard**. It does the
deterministic, testable work:

- **Discovery** (`internal/discover`): `gh pr list --json` per configured repo,
  then the New/Refreshed rules applied in-process. This replaced the original
  prompt's "set up a python script" step — native Go, no runtime script
  generation, unit-tested.
- **Queue + state** (`internal/store`): candidates, their queue positions,
  review history, and the per-repo approver allow-list in DuckDB. The review
  history is what makes **Refreshed** detection possible — a PR is Refreshed when
  its current head SHA differs from the SHA we last recorded a review at. That
  fact lives in our store, not gh.
- **Run-lock, ordering, parallelism cap** (`internal/scheduler`): skip a cycle
  if one is already running; process New-before-Refreshed, oldest-first, up to
  `max_parallel` (default 4) at a time.

The **review engine** (`internal/review`, default `codex`) is handed the
assembled prompt and left to do everything fuzzy: the review itself (typically
via the `pr-issue-review` skill), posting to GitHub, and any post-approve Slack
step. The approve/comment decision is a **built-in approval directive** in the
assembled prompt (`approvalDirective`) that defaults to comment-only — an
APPROVE is only ever permitted when the author is approvable for this repo AND
it isn't self-authored, so a missing/misconfigured rule can never accidentally
allow an approval. The comment-only case states no reason, avoiding a leak of
the gh user's identity. Other judgement-based behaviour (the Slack reaction
flow) is expressed as **prompt fragments** (config `review.rules`), not Go
control flow — keep tunable behaviour in the prompt, out of compiled code.

**Approval is data, per repo.** Who may be approved lives in the DuckDB
`approvers` table keyed by `(repo, github_handle)` (with `*` as a wildcard repo),
managed via the `approvers` command — not in config. At review time the scheduler
looks up whether the PR author is approvable for that PR's repo and passes only
that single author↔approvable pair into the prompt (`approverLine`). The full
list is never sent to the engine, matching the original spec's "don't share the
allow-list" constraint.

## Pluggable engine

`review.Engine` is an interface; `codex` is the only driver today, selected by
`review.engine`. A `claude` driver (or others) can drop in later without
touching the scheduler or store.

## Store driver: DuckDB via the `duckdb` CLI (subprocess), not embedded

Considered:

1. **Embedded `marcboeker/go-duckdb` (CGO).** Rejected — the family release
   pipeline (`shhac/homebrew-tap` shared workflow) cross-compiles with
   `CGO_ENABLED=0` from a single runner. A CGO driver can't ship through it.
2. **Pure-Go SQLite (`modernc.org/sqlite`).** Viable and arguably a better fit
   for a small OLTP queue, but it isn't DuckDB, which was the requested store.
3. **DuckDB via the `duckdb` CLI as a subprocess.** Chosen. Same pattern
   `agent-sql` already uses (`duckdb -cmd ".mode jsonlines" <path> -c "<sql>"`,
   parse NDJSON). Stays CGO-free, releases cleanly, keeps DuckDB.

Consequences: the `duckdb` binary is a runtime dependency (`brew install duckdb`;
override with `AGENT_CODE_REVIEW_DUCKDB_PATH`). DuckDB is single-writer per file,
so the driver serializes access with a mutex — fine at the daemon's scale (a
handful of reviews per cycle). The store stays behind the `store.Store`
interface so a pure-Go driver could be added if the runtime dep becomes painful.

## Runtime shape

`serve` is the primary entry point: an always-on daemon running the scheduler on
`schedule.interval` and hosting the dashboard, with `--tailscale serve|funnel`
wired through `lib-agent-mcp/tailscale`. `run --once` performs a single cycle for
external schedulers (launchd/cron) or manual kicks, honouring the same run-lock.

## Config is the only source of environment specifics

Every environment-specific thing — watched repos, age windows, cadence,
parallelism, the prompt + rules, the store path, dashboard and Tailscale
settings — lives in `~/.config/agent-code-review/config.json`. The approver
allow-list is the one exception: it's runtime data in the store, managed via
`approvers`. No GitHub handles or repos appear in code, docs, or the shipped
example config.

## Deliberately deferred

- **Structured verdicts.** `codex` currently infers APPROVE/COMMENT from the
  transcript (`parseDecision`). Better: read the verdict back from the posted
  GitHub review, or have the engine emit a structured result.
- **`mcp` subcommand.** The family convention includes one; omitted here because
  this tool is itself a server rather than a data CLI an agent calls. Easy to add
  later via `lib-agent-mcp`.
- **A second store driver.** The interface exists; only DuckDB is wired.
