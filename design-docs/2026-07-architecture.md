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

## Addendum — shipped later the same day (2026-07-07)

- **Structured verdicts landed** (superseding the first deferred item above),
  reframed after discussion: the *agent* performs the approve/comment on GitHub
  itself — so it can pair the decision with appropriate feedback comments — and
  then **reports back what it did** as a schema-constrained final message
  (`codex exec --output-schema` + `--output-last-message`, both verified against
  the real CLI). The driver parses `{decision: APPROVED|COMMENTED|SKIPPED,
  summary}`; `ERROR` remained driver-only. The scheduler records review history
  only for APPROVED/COMMENTED so skips/failures never masked Refreshed
  detection.
- **Live validation happened**: a codex smoke test drove the full driver path
  (passed, ~11s), and env-gated live discovery (`AGENT_CODE_REVIEW_TEST_REPO`)
  classified real `gh pr list` output correctly — team review requests carried
  `name` rather than `login`, as the structs assumed.
- **Discovery became per-repo resilient**: one failing repo was logged and
  skipped; discovery errored only when every repo failed.
- **Sandbox/network gotcha captured**: codex `workspace-write` disabled network
  by default, which would have broken every `gh` call. The starter config
  shipped `-c sandbox_workspace_write.network_access=true` (key verified against
  the real CLI) to re-enable network while keeping writes scoped to the per-PR
  workdir.
- **Dashboard grew review + run history** (`/api/reviews`, `/api/runs`);
  `config init` wrote the embedded starter (kept in lockstep with
  `config.example.json` by a test); `serve` warned that `--tailscale funnel`
  exposes the unauthenticated dashboard publicly.
- **`approvers` was renamed to `authors`** (`authors allow|deny|ls`, table
  `allowed_authors`, `IsAuthorAllowed`): the original name misread as "who can
  approve", when the list controls whose PRs *we* — the reviewer — will approve.
  The entity-named command group also left room for future author-scoped verbs.
- **Assumption boundary was drawn**: the tool assumes only the gh + codex CLIs
  (plus its own duckdb store dep). The shipped starter had leaked workspace
  knowledge (the pr-issue-review skill, agent-slack, emoji conventions in a
  `slack-on-approve` rule) — replaced by first-class post-outcome prompt slots
  (`review.on_approve` / `on_comment` / `on_reject`, reject = requested
  changes; verdict enum gained `REQUESTED_CHANGES`) shipped EMPTY. Workspace
  conventions belong in the user's config, never in defaults. For the same
  reason the starter's placeholder repos were dropped (`repos: []`) and a
  `repos ls|add|rm` command was added; a lockstep test bans skill/tool names
  reappearing in shipped prompts.
- **Dashboard queue-add accepted PR URLs** and gated adds to watched repos;
  CLI output was rerouted through lib-agent-output (the hand-rolled emit had
  silently ignored --color and -f json|yaml).
- **Codex usage reached the dashboard** via the app-server protocol: the codex
  CLI exposed no usage subcommand, but `codex app-server` (JSON-RPC over
  stdio, the desktop app's surface) answered `account/rateLimits/read` with
  the primary/weekly windows the TUI's /status shows. `internal/usage` spawns
  it per poll (default 10m, `dashboard.usage_poll_interval`), caches the
  snapshot, and `/api/usage` serves it. Protocol pinned at codex 0.138.0 —
  experimental surface, may drift. The Overview page became a two-panel hero:
  queue beside usage meters + a last-24h stacked hourly chart of
  approved/commented/requested-changes (`/api/stats`; colors validated with
  the dataviz six-checks in both light and dark modes), and the Config page
  showed the resolved "reviewing as @…" gh identity.

## Addendum 2 — feature batch (2026-07-07, later)

- **Cadences split into two independent loops**: the daemon had run discovery
  inside every review cycle; user feedback separated them fully. A new
  `discovery` config object ({enabled, interval}, default 10m) drives the
  deterministic gh sweep; `schedule` ({enabled, interval, max_parallel})
  drives review cycles — its own switch, and no parallelism dial where one
  makes no sense. Reaffirmed explicitly: candidate identification never
  involves the LLM — codex is invoked only per accepted review.
- **Manual adds fetch live metadata**: `queue add` and the dashboard POST had
  inserted bare rows that discovery would only backfill if the PR matched the
  candidate rules — exactly what a manual add often doesn't. Both paths now
  call `discover.ManualCandidate` (gh pr view → title/author/SHA), rejecting
  CLOSED/MERGED PRs.
- **Discovery gained two filters**: PRs whose GitHub `reviewDecision` is
  APPROVED are skipped (already unblocked — and deliberately keyed off the
  computed decision, not the raw reviews list, so a stale approval can't block
  a Refreshed re-review); repos listed in `allowed_authors_only_repos`
  (`repos add --allowed-authors-only`) only discover PRs from allowed authors.
- **Dashboard rows gained ✕ removal** (DELETE /api/queue), completing the
  add/reorder/remove management loop from the UI.

## Addendum 3 — observability + hot config (2026-07-07/08, v0.6.0–v0.7.x)

- **The dashboard became a mission-control app** (Svelte + Vite SPA, bundle
  committed and go:embed'd; CI's `dashboard-fresh` job rebuilds and diffs to
  keep it honest). Dark theme on a user-picked palette (Heavy Metal /
  Atlantis / Gallery / Edward; chart triad validated with the dataviz
  six-checks). Pages: Queue (drag-reorder worklist + usage meters + 24h
  outcome chart + paginated runs), History, per-review log, Config, Prompt,
  Logs (daemon log ring).
- **Queue/history split landed**: a queue row exists exactly while work is
  pending; `Complete` moves it into append-only history atomically in one
  DuckDB batch, DELETE gated on the reviewed head SHA so mid-review pushes
  survive for the next cycle. "Reviewing" is derived from a claim lease
  (`ClaimActive`, window `max(4×interval, 2h)` — the floor stops short
  intervals from shrinking the lease under a long review), never stored.
- **Usage floors**: the review loop pauses itself when a Codex rate-limit
  window has under `schedule.usage_floor.*` percent remaining (default 10,
  0 disables; fail-open on missing snapshots), checked before the run-lock so
  paused cycles record nothing.
- **Pre-review candidacy recheck**: discovered candidates are re-validated
  just before the engine spend (approved/closed/merged while queued →
  precheck SKIPPED); manual adds bypass it, so explicit requests always run.
- **Config went hot**: components hold `func() config.Config` getters; loops
  run on a 30s heartbeat evaluating a `due()` predicate against the LIVE
  interval, and the engine is rebuilt per cycle from that cycle's snapshot.
  Each operation snapshots config once and threads it (no torn reads). The
  boot `--no-*` flags stay pinned via a serve-level wrapper so a config edit
  cannot resurrect a disabled loop.
- **Live review logs**: the codex driver tees stdout+stderr into
  `<workdir>/agent.log` as the run progresses (buffer-only fallback keeps
  Verdict.Raw when the file can't be created). The claim records the workdir;
  history snapshots it. One resolver (`store.FindWorkspace`, queue row then
  last outcome) backs both `queue log [-f]` and `/api/review-log`, which
  serves the last 128KB tail. The dashboard's review-log page renders the
  stream as one bubble per event, parsed client-side from the codex exec
  format (marker lines `user`/`codex`/`exec`/`thinking`; parallel tool calls
  interleave result lines with no ids, so results pair FIFO — best-effort,
  with a raw view as ground truth). Format pinned at codex 0.138.0.
- **The output schema constrains EVERY assistant message**, not just the
  final one — observed live: all 17 intermediate messages of a real run were
  forced into verdict JSON, with the model overloading `SKIPPED` as "still
  working". The schema gained a `WORKING` decision for intermediate progress
  notes; the driver rejects a run that ENDS on WORKING (truncated) instead of
  recording a bogus outcome.
- **History grew metrics**: `duration_secs` (claim→completion; 0 = unknown,
  pre-feature rows backfilled) and `tokens_used` (parsed from codex's
  "tokens used" trailer; summed as all-time/24h on the usage panel). Columns
  arrive via idempotent trailing `ALTER TABLE ... IF NOT EXISTS` migrations
  applied at every Init — DuckDB can't add constrained columns, so the ALTERs
  carry DEFAULTs and the CREATEs keep NOT NULL for fresh stores.
- **The ldflags build version reached the Config page** so a browser can tell
  which daemon build is serving.

## Addendum 4 — eligibility holds + FIFO queue (2026-07-08)

Motivated by live-running observations: the review loop needed a long
interval purely to avoid re-burning tokens on the same few PRs, discovery
kept bumping `discovered_at` on every sweep, and the agent repeatedly
pounced on PRs the author was still actively working on (one fix of three
pushed → instant re-review; PR marked ready → reviewed before the final
rebase/title fix landed).

- **Eligibility holds on queue rows** (`eligible_at` + `hold_reason`,
  idempotent ALTER migration). Discovery computes the hold at classify time
  as the later of two bounds: **settling** (`candidates.quiet_period`,
  default 15m — the PR must go untouched that long; `updatedAt` was chosen
  over a GraphQL ReadyForReviewEvent lookup because it's already in the
  list payload and also covers mid-fix pushes on Refreshed candidates) and
  **cooldown** (`candidates.rereview_cooldown`, default 90m since our last
  real verdict — SKIPPED/ERROR deliberately don't cool down). Held rows are
  enqueued VISIBLY rather than silently dropped — the dashboard badges them
  with a countdown — and the scheduler's `availableCandidates` filter skips
  them via the shared `Candidate.Held` predicate. Holds only ever extend on
  re-sweep (an active author pushes the bound out), never shrink. `0s`
  disables either dial.
- **Promote became "review this now"**: one store write floats the row to
  the top, clears the hold, and escalates source to manual (bypassing the
  pre-review recheck) — CLI `queue promote` and dashboard ▶ share it.
  Decided explicitly: drag-reorder does NONE of that — moving a held row
  around the queue neither lifts its hold nor makes it manual.
- **`discovered_at` became first-seen** (the upsert had bumped it every
  sweep) and the queue order became **FIFO by first discovery** — a later
  sweep can never leapfrog work already waiting; New-before-Refreshed and
  PR number only break same-sweep ties. Explicit `queue_pos` still wins
  outright.
- **The review cadence default dropped 30m → 1m.** Holds + same-SHA
  suppression keep the queue empty of non-actionable work, and idle cycles
  now exit before the run-lock, recording no run row and logging nothing —
  previously each tick wrote a runs row, which at 1m would have been ~1.4k
  junk rows/day. The queue listing therefore moved ahead of StartRun in the
  cycle. LeaseWindow's 2h floor already kept short intervals from shrinking
  claim leases.

## Addendum 5 — crash + multi-instance resilience (2026-07-08, post-v0.9.0)

Motivated by two live-running observations: killing the daemon mid-review
left it refusing to cycle for the whole 2h lease window after restart ("a
previous run is still active — skipping"), and dev instances routinely run
beside the persistent daemon against the same store.

- **Boot reconciliation** (`Scheduler.Reconcile`, run at serve start and
  before `run --once`): run rows still `running` and queue claims whose
  recorded pid is dead *on this host* are released immediately (run →
  `failed`, claim → cleared) instead of waiting out the lease. Another
  host's state and any live pid — i.e. a sibling instance's in-flight work —
  are left strictly alone; the lease window remains the universal fallback.
- **Claims became compare-and-swap leases carrying host+pid**
  (`claim_host`/`claim_pid` columns; `Claim(ctx, repo, number, Lease)`
  returns whether you won). The UPDATE matches only unclaimed rows or claims
  older than `StaleAfter`, with `RETURNING` as the won/lost signal — one
  statement is one duckdb invocation, so the check-and-write is atomic under
  DuckDB's file lock even across instances. Losing is a clean skip: no
  engine spend, no outcome, closing the double-review hole two instances
  (or the run-lock's check-then-insert race) could hit.
- **`serve` binds the dashboard port before starting any loop.** The port
  doubles as the one-daemon-per-address guard; previously the loops fired
  concurrently with the bind, so a doomed second instance could claim and
  start reviewing during its first seconds. Now it exits with "is another
  serve instance already running?" before touching the queue.
- **Dev-boot guidance landed in AGENTS.md**: `serve --no-schedule` by
  default; opt into specific loops, or point at a scratch store, before
  enabling reviews on a dev instance.
- Verified live: a planted dead-pid run row + claim were reconciled at boot
  (run → failed, claim cleared) while a held row stayed held; a second
  serve on the same port exits at bind.
