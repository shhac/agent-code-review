# agent-code-review

PR review queue + scheduler for AI agents. Discovers candidate pull requests
across your repos, keeps a DuckDB-backed queue, and reviews each one by handing
an assembled prompt to a pluggable engine (default: Codex). Ships a dashboard
you can expose over Tailscale.

- **Deterministic discovery** — finds New and Refreshed candidate PRs via `gh`,
  no runtime scripts.
- **Durable queue** — candidates, positions, and review history in DuckDB, so
  "we already reviewed this at SHA X" survives restarts (that's what powers
  Refreshed detection).
- **Pluggable review engine** — `codex` today; the engine does the actual
  review (typically via the `pr-issue-review` skill), posts to GitHub, and runs
  any post-approve Slack step. Comment-only rules live in the prompt, not code.
- **Serve + dashboard** — an always-on daemon with a web UI, optionally exposed
  via `--tailscale serve|funnel`.
- **Everything is config** — repos, allow-list, thresholds, cadence, prompt, and
  rules all live in `config.json`. No GitHub handles or repos are hardcoded.

## Installation

```bash
brew install shhac/tap/agent-code-review
```

### Build from source

Requires Go 1.26+.

```bash
make build      # -> ./agent-code-review
```

### Runtime dependencies

- **`gh`** (GitHub CLI), authenticated — used for candidate discovery.
- **`duckdb`** CLI — the queue store (`brew install duckdb`; override the binary
  with `AGENT_CODE_REVIEW_DUCKDB_PATH`).
- **`codex`** — the default review engine (configurable).
- Optional: **`tailscale`** for `--tailscale serve|funnel`.

## Quick start

1. Write the starter config and edit it (`repos`, `review.main_prompt`, schedule):

   ```bash
   agent-code-review config init
   ```
2. Add the engineers whose PRs may be approved (per repo, or `*` for all):

   ```bash
   agent-code-review approvers add '*' some-handle --name "Some Engineer"
   ```
3. Kick a single cycle:

   ```bash
   agent-code-review run --once
   ```
4. Or run the daemon with the dashboard on your tailnet:

   ```bash
   agent-code-review serve --http :8330 --tailscale serve
   ```

## Command map

```
serve [--http :8330] [--tailscale serve|funnel] [--public-url URL] [--no-schedule]
run   [--once]

queue ls [--status S] [--repo R]
queue add     <owner/repo> <number>
queue rm      <owner/repo> <number>
queue promote <owner/repo> <number>
queue skip    <owner/repo> <number>

config init | path | show

approvers ls  [--repo R]
approvers add <owner/repo|*> <handle> [--name N --email E --slack-id ID]
approvers rm  <owner/repo|*> <handle>

usage
```

Global flags come from `lib-agent-cli`: `-f/--format`, `-t/--timeout`,
`-d/--debug`, `--color`.

## Candidate rules

- **NEW** — open, not draft, review requested, never reviewed by anyone, at most
  `candidates.new_max_age_days` old (default 14).
- **REFRESHED** — open, not draft, re-review requested, head SHA differs from the
  SHA we last recorded a review at, at most `candidates.refreshed_max_age_days`
  old (default 21).

Candidates are processed New-before-Refreshed, oldest PR first, up to
`schedule.max_parallel` (default 4) at a time.

## Approval allow-list

Who may be **approved** is decided per repo, and the list lives in the DuckDB
store (not config) so it can vary per repo and change at runtime:

```bash
agent-code-review approvers add owner/name alice --name "Alice" --slack-id U01
agent-code-review approvers add '*' bob            # bob is approvable on every repo
agent-code-review approvers ls --repo owner/name
```

At review time the CLI looks up whether the PR's author is approvable for that
PR's repo and passes **only that one author↔approvable pair** into the engine's
prompt — never the whole list.

## How review works

For each candidate the CLI assembles a prompt — your `review.main_prompt`, a
built-in **approval directive**, plus every matching `review.rules` fragment —
and hands it to the engine along with a tmp workspace. The engine (Codex)
performs the review itself and takes all the GitHub/Slack actions.

The approval directive is always present and **defaults to comment-only**. An
`APPROVE` is only ever permitted when the author is approvable for this repo
**and** it isn't a self-authored PR — never as a fallback when a rule happens to
be missing. In the comment-only case the directive gives no reason, so it can't
leak who the gh user is. `review.rules` are for *extra* instructions (e.g. the
post-approve Slack flow), not for the approve/comment decision.

## Configuration

`~/.config/agent-code-review/config.json` (respects `XDG_CONFIG_HOME`). See
`config.example.json` for the full shape: `repos`, `gh_user`, `candidates`,
`schedule`, `review` (engine + prompt + rules + codex), `store`, and `dashboard`
(addr + tailscale). The approver allow-list is **not** in config — it lives in
the store; manage it with `approvers`.

## Output

NDJSON on stdout — one JSON record per line. Errors go to stderr as
`{"error", "fixable_by", "hint"}` with a non-zero exit.

## Development

```bash
make build     # build the binary
make test      # go test ./...
make lint      # golangci-lint
make dev ARGS="queue ls"
```

Architecture and design decisions live in `design-docs/`.

## License

[PolyForm Perimeter 1.0.0](LICENSE).
