# agent-code-review

PR review queue + scheduler for AI agents. Discovers candidate pull requests
across your repos, keeps a DuckDB-backed queue, and reviews each one by handing
an assembled prompt to a pluggable engine (default: Codex). Ships a dashboard
you can expose over Tailscale.

- **Deterministic discovery**: finds New and Refreshed candidate PRs via `gh`
  on its own cadence (`discovery.interval`, with its own `discovery.enabled` switch), never involving the LLM;
  already-approved PRs are skipped, and repos can be scoped to allowed authors
  only (`repos add --allowed-authors-only`).
- **Durable queue**: candidates, positions, and review history in DuckDB, so
  "we already reviewed this at SHA X" survives restarts (that's what powers
  Refreshed detection).
- **Pluggable review engine**: `codex` today; the agent does the actual
  review, posts to GitHub, and reports back what it did. The tool assumes only
  the `gh` and `codex` CLIs; your prompts may direct the agent to use anything
  else you have set up (skills, extra CLIs), but the tool never assumes it.
- **Serve + dashboard**: an always-on daemon with a web UI, optionally exposed
  via `--tailscale serve|funnel`.
- **Everything is config**: repos, allow-list, thresholds, cadence, prompt, and
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

- **`gh`** (GitHub CLI), authenticated. Used for candidate discovery.
- **`duckdb`** CLI: the queue store (`brew install duckdb`; override the binary
  with `AGENT_CODE_REVIEW_DUCKDB_PATH`).
- **`codex`**: the default review engine (configurable).
- Optional: **`tailscale`** for `--tailscale serve|funnel`.

These are the tool's ONLY assumptions. Anything your prompts reference beyond
them (skills, `agent-*` CLIs, team tooling) is your prompts' business; the
tool neither requires nor mentions it.

## Quick start

1. Write the starter config:

   ```bash
   agent-code-review config init
   ```
2. Add the repos to watch and the authors whose PRs may be approved:

   ```bash
   agent-code-review repos add your-org/your-repo
   agent-code-review authors allow '*' some-handle --name "Some Engineer"
   ```
3. Set your prompts and dials, then kick a single cycle:

   ```bash
   agent-code-review prompts set on-approve "Notify the team per your conventions."
   agent-code-review config set schedule.interval 15m
   agent-code-review run --once
   ```

   Every command group has a `usage` subcommand with full docs and examples
   (`repos usage`, `authors usage`, `prompts usage`, `config usage`, `queue usage`).
4. Or run the daemon with the dashboard on your tailnet:

   ```bash
   agent-code-review serve --http :8330 --tailscale serve
   ```

## Command map

```
serve [--http :8330] [--tailscale serve|funnel] [--public-url URL]
      [--no-discovery] [--no-reviews] [--no-schedule]
run   [--once]

queue ls [--repo R]
queue add     <owner/repo> <number>
queue rm      <owner/repo> <number>
queue promote <owner/repo> <number>
queue skip    <owner/repo> <number>

repos ls | add <owner/repo> | rm <owner/repo>

prompts show | set <slot> <text> | unset <slot> | preview [--author-not-allowed]

config init | path | show
config list | get <key> | set <key> <value> | unset <key>

authors ls    [--repo R]
authors allow <owner/repo|*> <handle> [--name N --email E --slack-id ID]
authors deny  <owner/repo|*> <handle>

usage
```

Global flags come from `lib-agent-cli`: `-f/--format`, `-t/--timeout`,
`-d/--debug`, `--color`.

## Candidate rules

- **NEW**: open, not draft, review requested, never reviewed by anyone, at most
  `candidates.new_max_age_days` old (default 14).
- **REFRESHED**: open, not draft, re-review requested, head SHA differs from the
  SHA we last recorded a review at, at most `candidates.refreshed_max_age_days`
  old (default 21).

Candidates are processed New-before-Refreshed, oldest PR first, up to
`schedule.max_parallel` (default 4) at a time.

## Allowed authors

We are the reviewer. This list controls **whose PRs we will approve** (not who
can approve). Decided per repo, and stored in DuckDB (not config) so it can vary
per repo and change at runtime:

```bash
agent-code-review authors allow owner/name alice --name "Alice" --slack-id U01
agent-code-review authors allow '*' bob            # bob's PRs approvable on every repo
agent-code-review authors ls --repo owner/name
agent-code-review authors deny owner/name alice    # back to comment-only
```

At review time the CLI looks up whether the PR's author is allowed for that
PR's repo and passes **only that one author↔allowed pair** into the engine's
prompt, never the whole list.

## How review works

For each candidate the CLI assembles a prompt (your `review.main_prompt`, a
built-in **approval directive**, your post-outcome instructions, plus every
matching `review.rules` fragment) and hands it to the engine along with a tmp
workspace. The agent performs the review itself, takes all the GitHub actions,
and reports back what it did (`APPROVED`, `COMMENTED`, `REQUESTED_CHANGES`, or
`SKIPPED`) so the queue and history stay accurate.

The approval directive is always present and **defaults to comment-only**. An
`APPROVE` is only ever permitted when the author is on the allowed-authors list
for this repo **and** it isn't a self-authored PR, never as a fallback when a
rule happens to be missing. In the comment-only case the directive gives no
reason, so it can't leak who the gh user is.

**Post-outcome instructions** (`review.on_approve`, `review.on_comment`,
`review.on_reject`) tell the agent what to do after landing on each outcome
(reject = requested changes). This is where workspace-specific conventions
live (team channels, emoji rituals, notification tooling); the tool ships
none of that. `review.rules` add further conditional instructions (per repo,
per candidate type).

## Configuration

`~/.config/agent-code-review/config.json` (respects `XDG_CONFIG_HOME`). See
`config.example.json` for the full shape: `repos`, `gh_user`, `candidates`,
`schedule`, `review` (engine + prompt + rules + codex), `store`, and `dashboard`
(addr + tailscale). The allowed-authors list is **not** in config; it lives in
the store; manage it with `authors`.

## Dashboard

`serve` hosts a small web UI (default `:8330`) with three pages:

- **Overview**: a two-panel hero with the queue (add via pasted PR URL or
  `owner/repo/pull/N`; live title/author fetched on add, closed/merged PRs
  and unwatched repos rejected; drag-to-reorder; ✕ removal) beside **Codex usage meters** (5h + weekly windows, polled
  every `dashboard.usage_poll_interval`, default 10m) and a **last-24h chart**
  of approved / commented / changes-requested outcomes per hour. Recent
  reviews and runs below. Auto-refreshes.
- **Config**: watched repos, resolved settings, and the allowed-authors list.
  Read-only.
- **Prompt**: the main prompt, the rules, and a fully assembled preview of
  what the agent receives (allowed vs not-allowed author variants). Read-only.

Queue add/reorder are also available as JSON endpoints (`POST /api/queue`,
`POST /api/queue/reorder`). The dashboard has no auth, so keep it on your tailnet
(`--tailscale serve`) unless you mean to expose it.

## Output

NDJSON on stdout, one JSON record per line. Errors go to stderr as
`{"error", "fixable_by", "hint"}` with a non-zero exit.

## Development

```bash
make build     # build the binary
make dashboard # rebuild embedded dashboard assets
make test      # go test ./...
make lint      # golangci-lint
make dev ARGS="queue ls"
```

Architecture and design decisions live in `design-docs/`.

## License

[PolyForm Perimeter 1.0.0](LICENSE).
