# agent-code-review

PR review queue + scheduler for AI agents. Discovers candidate pull requests
across your repos, keeps a DuckDB-backed queue, and reviews each one by handing
an assembled prompt to a pluggable engine (Codex or Claude Code; default:
Codex). Ships a dashboard
you can expose over Tailscale.

- **Deterministic discovery**: finds New and Refreshed candidate PRs via `gh`
  on its own cadence (`discovery.interval`, with its own `discovery.enabled` switch), never involving the LLM;
  already-approved PRs are skipped, and an author group can take a repo's
  unrostered authors out of discovery entirely (`repos add --unlisted ...`).
- **Eligibility holds**: discovered candidates wait out a **quiet period**
  (`candidates.quiet_period`, default 15m: don't review a PR mid-rebase or
  mid-fix push) and a **re-review cooldown** (`candidates.rereview_cooldown`,
  default 90m: give the author room to respond to the last review). Held PRs
  sit visibly in the queue and are not dispatched until eligible;
  `queue promote` or a manual add bypasses holds. This is what makes a tight
  review cadence cheap: only genuinely actionable work spends tokens.
- **Durable queue**: candidates, positions, and review history (verdict,
  duration, token spend, workspace) in DuckDB, so "we already reviewed this at
  SHA X" survives restarts (that's what powers Refreshed detection).
- **Pluggable review engine**: `codex` (default) or `claude`; the agent does
  the actual review, posts to GitHub, and reports back what it did. Both report
  through the same verdict contract and write the same review log, so switching
  is a one-key config change. The tool assumes only the `gh` CLI plus whichever
  engine CLI you selected; your prompts may direct the agent to use anything
  else you have set up (skills, extra CLIs), but the tool never assumes it.
- **Live review logs**: the engine tees its output into the review workspace,
  so an in-flight review can be watched via `queue log -f` or the dashboard's
  per-review page (and read back after it finishes).
- **Serve + dashboard**: an always-on daemon with a web UI, optionally exposed
  via `--tailscale serve|funnel`. Most config edits (cadence, parallelism,
  usage floors, repos, prompts, engine settings) reload live within ~30s; only
  the loop on/off switches and the listen/Tailscale settings need a restart.
- **Usage floors**: the review loop pauses itself when the configured
  engine's rate-limit window has less than `schedule.usage_floor.*` percent
  remaining (default 10), and resumes when the window refills. The floor
  follows the configured engine, since that is the account reviews spend from;
  the dashboard meters **every** engine side by side so you can see whether
  the one you're not using has more headroom before switching.
- **Per-review spend**: every review records its token count, and its
  API-rate cost where the engine reports one, so a per-review budget can be
  set from your own data rather than guessed.
- **Everything is config**: repos, author groups, thresholds, cadence, prompt, and
  rules all live in `config.json`. No GitHub handles or repos are hardcoded.

## Installation

```bash
brew install shhac/tap/agent-code-review
```

### Claude Code / AI agent skill

```bash
npx skills add shhac/agent-skills --skill agent-code-review --global
```

Installs the `agent-code-review` skill globally so Claude Code (and other AI
agents) can discover and use it automatically. It ships from
[`shhac/agent-skills`](https://github.com/shhac/agent-skills) — the whole
family's skills in one repo, so `npx skills update` checks a single source no
matter how many you use. Want several at once? Run `npx skills add
shhac/agent-skills --global` and pick from the list.

### Build from source

Requires Go 1.26+.

```bash
make build      # -> ./agent-code-review
```

### Runtime dependencies

- **`gh`** (GitHub CLI), authenticated. Used for candidate discovery.
- **`duckdb`** CLI: the queue store (`brew install duckdb`; override the binary
  with `AGENT_CODE_REVIEW_DUCKDB_PATH`).
- **`codex`** or **`claude`**: the review engine, whichever `review.engine`
  selects (default `codex`). Only the selected one is needed, and it must
  already be authenticated: this tool never handles engine credentials.

Run `agent-code-review doctor` to check all of the above at once. Each of
these otherwise fails only at review time, as an `ERROR` history row whose
cause is buried in the engine transcript; `doctor` exits non-zero on a
blocking failure, and `serve` runs the same checks at boot and logs them.
- Optional: **`tailscale`** for `--tailscale serve|funnel`.

These are the tool's ONLY assumptions. Anything your prompts reference beyond
them (skills, `agent-*` CLIs, team tooling) is your prompts' business; the
tool neither requires nor mentions it.

## Quick start

1. Write the starter config:

   ```bash
   agent-code-review config init
   ```
2. Add the repos to watch and put people in groups:

   ```bash
   agent-code-review repos add your-org/your-repo
   agent-code-review authors set '*' some-handle approver --name "Some Engineer"
   ```
3. Set your prompts and dials, then kick a one-shot run:

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

repos ls | add <owner/repo> [--unlisted <group>] | rm <owner/repo>

prompts show | set <slot> <text> | unset <slot> | preview [--author H] [--group G]

config init | path | show
config list | get <key> | set <key> <value> | unset <key>

authors ls     [--repo R] [--group G]
authors set    <owner/repo|*> <handle> <group> [--name N --email E --slack-id ID]
authors rm     <owner/repo|*> <handle>
authors groups
authors who    <handle> --repo <owner/repo>

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

Held rows stay visible in the queue (badged, with a countdown) but are not
dispatched until `eligible_at`. Sweeps only ever *extend* a hold, never
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

Reviews are dispatched one at a time as slots free rather than in batches: the
moment a review finishes, the next ready candidate is picked up after
`schedule.dispatch_cooldown` (default 5s). Nothing waits for a batch to
drain, so a PR discovered while other reviews are in flight starts as soon as
there is room for it. `schedule.interval` (default 1m) is only the idle poll,
for when a look at the queue found nothing ready.

There is no global run-lock. Two reviewers can never take the same PR (the
queue claim is a compare-and-swap, store-wide), but `run` and a live daemon do
otherwise work the same queue in parallel.

## Author groups

We are the reviewer. An author belongs to **one group per repo**, and the group
is a complete review policy: what we may do with their PRs, which engine does
it, and what extra instruction the agent gets.

Review level is an ordered ladder:

| level | meaning |
| --- | --- |
| `ignore` | never discovered (a manual `queue add` still reviews them) |
| `comment` | reviewed, never approved |
| `approve` | approvable when the review warrants it |

Groups are defined in config, beside the prompts they carry:

```jsonc
"authors": {
  // Where an author with no roster row lands, per repo. "*" is the fallback.
  "unlisted": { "*": "outsider", "owner/infra": "nobody" },

  "groups": {
    "core":     { "review": "approve", "engine": "claude", "model": "opus", "effort": "high" },
    "outsider": { "review": "comment", "prompt": "State our conventions explicitly." },
    "nobody":   { "review": "ignore" }
  },

  // Narrower than a group: patches fields onto whatever group resolved.
  "overrides": [
    { "handle": "bob", "repos": ["owner/name"], "model": "claude-opus-5", "effort": "medium",
      "prompt": "Open every post with one sentence addressing them as Lizard Elder." }
  ]
}
```

Who is in each group is roster data: it churns and varies per repo, so it
lives in DuckDB rather than config.

```bash
agent-code-review authors set owner/name alice core --name "Alice" --slack-id U01
agent-code-review authors set '*' bob outsider     # that group on every repo
agent-code-review authors ls --repo owner/name     # rows + the policy each resolves to
agent-code-review authors groups                   # the cohorts and what each grants
agent-code-review authors who alice --repo owner/name
agent-code-review authors rm owner/name alice      # back to authors.unlisted
```

Resolution is two steps. First the group: the roster row for this repo, else
the row for `*`, else `authors.unlisted[repo]`, else `authors.unlisted["*"]`.
Then the fields: the group, then every matching override in config order, one
field at a time, where empty inherits and prompt fragments accumulate.

`authors who` prints the layer that decided each field, which is the answer to
"why did that PR get approved / ignored". At review time only this PR's own
resolved policy reaches the engine, never the roster.

Configs written before groups keep working untouched: existing allow-list rows
resolve to the built-in `approver` group, and `allowed_authors_only_repos`
still means what it meant.

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
`APPROVE` is only ever permitted when the author's resolved group is at the
`approve` level **and** it isn't a self-authored PR, never as a fallback when a
rule happens to be missing. The self-review veto sits above the group cascade:
no group and no override can grant approving your own PR. In the comment-only
case the directive gives no reason, so it can't leak who the gh user is.

**Post-outcome instructions** (`review.on_approve`, `review.on_comment`,
`review.on_reject`) tell the agent what to do after landing on each outcome
(reject = requested changes). This is where workspace-specific conventions
live (team channels, emoji rituals, notification tooling); the tool ships
none of that. A group's own `prompt` covers the unconditional per-cohort case;
`review.rules` add further conditional instructions (per group, per handle, per
repo, per candidate type, optionally scoped to one outcome).

Because a group can name its own engine, concurrent reviews can run both. The
usage floor is therefore applied **per engine**: when one is out of headroom
its candidates wait in the queue like any other hold, while candidates bound
for the other engine run as normal.

## Configuration

`~/.config/agent-code-review/config.json` (respects `XDG_CONFIG_HOME`). See
`config.example.json` for the full shape: `repos`, `gh_user`, `candidates`,
`schedule`, `review` (engine + prompt + rules + codex/claude), `authors`
(groups + unlisted + overrides), `store`, and `dashboard` (addr + tailscale).
Group *membership* is **not** in config; it lives in the store; manage it with
`authors set`.

## Dashboard

`serve` hosts a small web UI (default `:8330`):

- **Queue**: the pending worklist (add via pasted PR URL or
  `owner/repo/pull/N`; live title/author fetched on add, closed/merged PRs
  and unwatched repos rejected; drag-to-reorder; ✕ removal). A reviewing
  badge links to that review's live log page; held rows show an on-hold badge
  with the reason and a countdown, plus a ▶ "review now" action that clears
  the hold (reordering alone never does). Beside it: **engine usage
  meters** (labelled with the engine being metered) (5h + weekly windows, polled every `dashboard.usage_poll_interval`,
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
