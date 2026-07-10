# agent-code-review

PR review queue + scheduler for AI agents. Discovers candidate pull requests
across your repos, keeps a DuckDB-backed queue, and reviews each one by handing
an assembled prompt to a pluggable engine (default: Codex). Ships a dashboard
you can expose over Tailscale.

- **Deterministic discovery**: finds New and Refreshed candidate PRs via `gh`
  on its own cadence (`discovery.interval`, with its own `discovery.enabled` switch), never involving the LLM;
  already-approved PRs are skipped, and repos can be scoped to allowed authors
  only (`repos add --allowed-authors-only`).
- **Eligibility holds**: discovered candidates wait out a **quiet period**
  (`candidates.quiet_period`, default 15m: don't review a PR mid-rebase or
  mid-fix push) and a **re-review cooldown** (`candidates.rereview_cooldown`,
  default 90m: give the author room to respond to the last review). Held PRs
  sit visibly in the queue and are skipped by review cycles until eligible;
  `queue promote` or a manual add bypasses holds. This is what makes a tight
  review cadence cheap: only genuinely actionable work spends tokens.
- **Durable queue**: candidates, positions, and review history (verdict,
  duration, token spend, workspace) in DuckDB, so "we already reviewed this at
  SHA X" survives restarts (that's what powers Refreshed detection).
- **Pluggable review engine**: `codex` today; the agent does the actual
  review, posts to GitHub, and reports back what it did. The tool assumes only
  the `gh` and `codex` CLIs; your prompts may direct the agent to use anything
  else you have set up (skills, extra CLIs), but the tool never assumes it.
- **Live review logs**: the engine tees its output into the review workspace,
  so an in-flight review can be watched via `queue log -f` or the dashboard's
  per-review page (and read back after it finishes).
- **Serve + dashboard**: an always-on daemon with a web UI, optionally exposed
  via `--tailscale serve|funnel`. Most config edits (cadence, parallelism,
  usage floors, repos, prompts, codex settings) reload live within ~30s; only
  the loop on/off switches and the listen/Tailscale settings need a restart.
- **Usage floors**: the review loop pauses itself when a Codex rate-limit
  window has less than `schedule.usage_floor.*` percent remaining (default
  10), and resumes when the window refills.
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
   agent-code-review config set candidates.rereview_cooldown 2h
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
queue log     <owner/repo> <number> [-f|--follow]

repos ls | add <owner/repo> [--allowed-authors-only] | rm <owner/repo>

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

In both cases the PR must not be currently approved (it's already unblocked),
and any recorded outcome (review, skip, or error) at the PR's current head
SHA suppresses re-enqueueing until new commits change the SHA.

Discovered candidates can carry an **eligibility hold**, computed at discovery
time as the later of two bounds and stored on the queue row (`eligible_at` +
`hold_reason`):

- **settling**: the PR was pushed to or edited within
  `candidates.quiet_period` (default 15m). Authors often mark a PR ready and
  then rebase once more or fix the title; every update pushes the bound out.
- **cooldown**: we posted a real review within
  `candidates.rereview_cooldown` (default 90m). The common rhythm is "agent
  requests changes, author fixes finding 1 of 3 and pushes"; without the
  cooldown, that first push would immediately burn a re-review.

Held rows stay visible in the queue (badged, with a countdown) but review
cycles skip them until `eligible_at`. Sweeps only ever *extend* a hold, never
shrink one. Set either dial to `0s` to disable it. `queue promote` (or the
dashboard's ▶) clears the hold, floats the PR to the top, and treats it as a
manual add; plain drag-reorder changes only the position and never lifts a
hold. `discovered_at` records the *first* sweep that saw the pending work and
is never bumped by later sweeps.

Candidates are processed FIFO by first discovery (a later sweep can never
leapfrog PRs already waiting; New-before-Refreshed and PR number break ties
within one sweep), up to `schedule.max_parallel` (default 4) at a time. Just
before the engine runs, discovered candidates are re-checked: PRs approved,
closed, or merged while waiting in the queue complete as a precheck SKIPPED
instead of spending a review. Manual adds (`queue add`, dashboard) bypass
that recheck; an explicit request always goes through.

The review loop itself defaults to a tight `schedule.interval` of 1m: idle
cycles (nothing eligible) are free no-ops that record nothing, so the cadence
costs nothing while holds and same-SHA suppression keep the queue quiet.

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
`SKIPPED`) so the queue and history stay accurate. History records the
verdict, how long the review took, and the token spend; the engine tees its
output into the workspace's `agent.log`, watchable live with
`queue log <owner/repo> <n> --follow` or the dashboard's per-review page.

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

`serve` hosts a small web UI (default `:8330`):

- **Queue**: the pending worklist (add via pasted PR URL or
  `owner/repo/pull/N`; live title/author fetched on add, closed/merged PRs
  and unwatched repos rejected; drag-to-reorder; ✕ removal). A reviewing
  badge links to that review's live log page; held rows show an on-hold badge
  with the reason and a countdown, plus a ▶ "review now" action that clears
  the hold (reordering alone never does). Beside it: **Codex usage
  meters** (5h + weekly windows, polled every `dashboard.usage_poll_interval`,
  default 10m) with total token spend, a **last-24h chart** of
  approved / commented / changes-requested outcomes per hour, and paginated
  recent runs. Auto-refreshes.
- **History**: every recorded outcome (approvals, comments, change requests,
  skips, errors) with duration, token spend, and a link to each review's log.
- **Review log** (`/review/<owner>/<repo>/<n>`): the agent's output as one
  bubble per event (prompt, agent messages, commands with status and
  duration) tailing live while the review runs, with a raw view toggle.
- **Config**: daemon version, watched repos, resolved settings, and the
  allowed-authors list. Read-only.
- **Prompt**: the main prompt, the rules, and a fully assembled preview of
  what the agent receives (allowed vs not-allowed author variants). Read-only.
- **Logs**: a live tail of the daemon's own log.

Queue add/reorder/promote are also available as JSON endpoints
(`POST /api/queue`, `POST /api/queue/reorder`, `POST /api/queue/promote`). The dashboard has no auth, so keep it on your tailnet
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
