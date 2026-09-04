# agent-code-review

PR review queue + scheduler for AI agents. Go + cobra on the `lib-agent-*`
family libraries, compiled to a standalone (CGO-free) binary.

## Architecture

```
cmd/agent-code-review/main.go   # entry point; version injected via -ldflags
internal/
├── cli/
│   ├── root.go                 # lib-agent-cli NewRoot; registers subcommands
│   ├── deps.go                 # buildScheduler (engine + sweeper + gh user); emit()
│   ├── serve.go                # `serve` daemon: scheduler + dashboard + tailscale.Wire
│   ├── shutdown.go             # the two-stage stop: graceful, then forced
│   ├── pricing.go              # estimator + costRates: one valuation, two paths
│   ├── run.go                  # `run`: discover, drain the queue, exit
│   ├── queue.go                # `queue ls/add/rm/promote/skip/log`
│   ├── authors.go              # `authors set/rm/ls/groups/who`: the author roster
│   ├── repos.go                # `repos ls/add/rm`: the watched repos (config)
│   ├── prompts.go              # `prompts show/set/unset/preview`: review prompts
│   ├── configcmd.go            # `config init/path/show/list/get/set/unset`
│   └── usage.go                # top-level LLM reference card
├── config/                     # ~/.config/agent-code-review/config.json + resolved defaults
├── store/                      # Store interface + DuckDB subprocess driver + schema.sql
├── discover/                   # gh pr list → New/Refreshed/Discussion classification
├── review/                     # Engine interface + codex/claude drivers + prompt/rule assembly
├── scheduler/                  # discovery loop, review dispatcher, parallelism cap, claim leases
│   ├── scheduler.go            # Deps + New: the seam declarations and composition root
│   ├── lifecycle.go            # StartGraceful (daemon) and RunOnce (`run`)
│   ├── dispatch.go             # the consumer loop: pull, hand off, cool down
│   ├── dispatchstate.go        # per-candidate in-flight/backoff bookkeeping
│   ├── loop.go                 # the interval loop (discovery's only)
│   ├── discover.go             # the sweep + its in-flight guard
│   ├── review.go               # reviewOne: claim, recheck, engine, record
│   └── reconcile.go            # release a crashed daemon's claims on this host
├── usage/                      # per-engine subscription-headroom polling + usage-floor predicate
├── doctor/                     # preflight: gh/duckdb/engine binary, auth, and config sanity
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
  never as Go control flow. The tool assumes only the gh CLI plus the selected
  engine's CLI; skills and extra CLIs are user-prompt territory. See
  `design-docs/2026-07-architecture.md`.

- **Engines differ only in how they spawn a CLI.** `review/driver.go` holds
  everything engine-agnostic: the verdict schema, the reporting instruction,
  the agent-log sink, and the bounded resume policy (`resumableRun`). A driver
  supplies just its argv builders and how to read a session id, a token split,
  and a report back out. Both engines are driven in JSON mode and both render
  their own stream into the SAME marker transcript (`codexstream.go`,
  `claudestream.go`), so `agent.log` stays one format and the dashboard needs
  one parser. They differ in how the report comes back: `codex exec` writes it
  to a file (`--output-last-message`), while `claude -p` reports in-stream
  (`--json-schema`, delivered as a forced `StructuredOutput` tool call). That
  cross-language contract is pinned by a golden fixture per engine
  (`review/testdata/{codex,claude}-transcript.golden`) written by the Go tests
  and read by `ui/src/lib/agentlog.test.ts`; regenerate with
  `go test ./internal/review -update-golden`.

- **The engine subprocess must leave our process group, or Ctrl-C is not
  graceful.** A terminal delivers SIGINT to the whole FOREGROUND PROCESS
  GROUP, and a child inherits its parent's group, so the first Ctrl-C reached
  the engine directly and killed reviews that were minutes and over a million
  tokens in. The context plumbing was never consulted: gracefulCtx/reviewCtx
  are correct, the signal just arrived somewhere else first, and every
  interrupted review recorded ERROR with its spend already gone. Engines are
  therefore started with `Setpgid`, and cancellation kills the negative pid so
  the whole group (engines spawn shells, toolchains, gh) goes with it. Only
  once the engine is out of the terminal's reach does the graceful/force split
  mean anything. Cheap subprocesses (gh, duckdb, version probes) stay in the
  group deliberately: dying on Ctrl-C is the right behaviour for them.

- **Cancellation is checked before the semaphore, not inside the same select.**
  `select` picks uniformly at random among ready cases and a free parallelism
  slot is almost always ready, so leaving "should we start another review" to
  the select alone launched new engine invocations roughly half the time after
  shutdown was requested. A coin flip is not an acceptable answer to a
  question that costs a full review.

- **The positional prompt goes behind a `--` terminator.** `claude`'s
  `--allowedTools` is VARIADIC (`<tools...>`), so it keeps consuming argv until
  the next flag. With the prompt merely appended last it was swallowed as one
  more tool name and every run died on "Input must be provided either through
  stdin or as a prompt argument". It only bit the static permission modes,
  because the fallback tool list is skipped in auto mode and auto is the
  shipped default, so the failure was invisible in normal use. Ordering is not
  the fix: the argv ends with the user's own `claude.args`/`codex.args`, which
  may hold any flag at all. A test asserting "the prompt is last" passes while
  this is broken; the invariant worth pinning is that nothing before the prompt
  can claim it. codex's own flags are all single-value today, but it does have
  a variadic `-i/--image`, so the same hazard applies to anything added there.

- **An interrupted review is recovered, not repeated.** Killing the daemon
  mid-review never loses the PR: the queue row survives (only `Complete`
  retires it), `Reconcile` releases claims held by a dead pid on this host,
  and the lease reclaims anything it cannot judge. What used to be lost was
  the *work*. Reconcile now records the abandoned attempt through
  `AppendHistory` (an insert that deliberately leaves the queue row pending,
  unlike `Complete`), keeping its `work_dir` and so its transcript reachable.
  The re-claim then reads the session id back out of that transcript
  (`review.SessionFromLog`) and hands it to the driver, which opens with a
  resume and the nudge instead of paying for the review again from cold. The
  transcript is the ONLY place a session id survives a daemon death, which is
  why both engines render it into the shared log format rather than keeping it
  in memory. Nothing to resume degrades to a normal fresh review.

- **Not reviewing twice is ours, not GitHub's.** An attempt interrupted after
  it posted recorded nothing. It happened not to double-post only because
  GitHub clears the review request when a requested reviewer submits, which is
  incidental and fails for team requests. The recheck now asks directly:
  `gh` returns `commit.oid` per review, so "have we already reviewed THIS
  revision" is exact. Per revision, not per PR, so new commits stay reviewable;
  manual queue adds bypass the recheck, so a deliberate re-review still works.

- **The two engines report usage with opposite scopes, and it is measured.**
  claude's usage is PER-INVOCATION, so its transcoder sums; codex's
  `turn.completed` carries the SESSION TOTAL every turn, so its transcoder
  replaces. Summing codex (which the old prose-trailer parser did)
  double-counts every resumed run. codex's `input_tokens` also INCLUDES its
  cached reads, where claude reports them apart. These are engine facts, so
  each driver states its own mapping onto `TokenUsage` and nothing downstream
  branches on the engine. Both are pinned by tests carrying the live
  measurements that established them.

- **Model prices come from LiteLLM, cached, never vendored.** Only claude
  values its own runs; codex reports no cost anywhere, so its spend has to be
  derived. `internal/pricing` keeps a copy of LiteLLM's price database in the
  app's CACHE dir (`xdg.CacheDir`) rather than its data dir: it is
  re-fetchable, so losing it costs a download rather than a record, and
  nothing bundles the file into the binary. The daemon polls every 6h with a
  conditional GET on the stored ETag, so an unchanged database costs a 304
  with an empty body instead of 1.6MB. A refresh parses before it writes and
  swaps in by rename, so a truncated or reshaped download leaves the last good
  copy intact. Pricing is an enrichment: an absent or stale table costs an
  estimate, never a review, which is why its doctor check is non-blocking.

- **Two spend figures per review, one rule.** `cost_usd` is what the engine
  reported (claude only); `est_cost_usd` is our valuation of the same run's
  token classes at the model's rates. `EffectiveCostUSD` is reported-wins,
  estimate-fills-the-gap, and it is deliberately expressible in SQL
  (`COALESCE(NULLIF(cost_usd, 0), est_cost_usd)`) — the fresh-token heuristic
  it echoes was not, which is how a Go aggregate and a SQL one came to
  disagree by 28x. Estimates are frozen at completion, and the boot backfill
  only ever fills a gap, so today's rates never rewrite what a past review
  cost. We estimate claude too even though it reports: the two figures side by
  side are the only check that our class mapping and rates are right, and the
  metrics summary surfaces that drift. 0 with no estimate means unknown, never
  free — aggregates count priced reviews separately so an inferred total
  cannot pass as a measured one.

- **Token classes are recorded apart because they are priced apart.** A cached
  read costs about a tenth of fresh input and a sixtieth of output, so a
  blended figure cannot be priced. `history` keeps input/output/cache-write/
  cache-read/reasoning plus `fresh_tokens` (the only cross-engine comparable
  figure) and `usage_raw`, the engine's verbatim payload. `usage_raw` is the
  escape hatch: claude reports 5m/1h cache-write tiers priced differently and
  separately-billed server tool calls that are not modelled, so a later
  pricing question is a query rather than a migration and a data gap.

- **The claude engine defaults to auto permission mode, on purpose.** A review
  is open-ended tool work, so enumerating tools up front contradicts "the
  engine owns everything fuzzy". Auto mode routes each action through a
  classifier instead. It is also the better security posture: a PR's diff,
  description, and comments are untrusted input, and the classifier reads user
  messages, tool calls, and CLAUDE.md but NOT tool results, so instructions
  smuggled into a PR cannot talk it into approving an action. Consequences to
  keep in mind: allow rules resolve BEFORE the classifier, so
  `claude.allowed_tools` must stay empty in auto mode or it exempts exactly
  what should be vetted (the static modes fall back to a gh-plus-reads floor
  instead, since they cannot reach gh on their own); auto mode needs Opus
  4.6+, Sonnet 4.6+, or Fable 5, so pinning `claude.model` to haiku breaks
  every review; and under `-p` there is nobody to prompt, so repeated
  classifier blocks abort the run rather than falling back.

- **Usage metering covers every engine; the floor follows the configured one.** `usage.Source` picks the
  reader: codex speaks JSON-RPC to `codex app-server`; claude reads the
  account's OAuth usage endpoint, since Claude Code exposes no usage command
  and reports no headroom in its run output. Both map onto the same
  `Snapshot`, so `schedule.usage_floor` and the dashboard panel are engine-
  agnostic. Every path fails open: an errored snapshot never pauses reviews,
  because review availability must not depend on the meter working. The
  claude reader touches a stored credential; it must never log it, copy it
  into a Snapshot, or include it in an error string.

  EVERY engine is polled, not just the configured one, so the dashboard can
  show both side by side and an operator can see the engine they are not using
  has headroom before switching. The usage FLOOR still consults only the
  configured engine, since that is the account reviews spend from. A failed
  poll keeps being retried and reports "unavailable: <reason>" in its slot; it
  is never dropped, because a missing slot reads as "this engine does not
  exist". `Snapshot.OK()` is the availability test: a failed poll still stamps
  FetchedAt, so "we tried" and "we have numbers" are different questions.

  Distinct from usage: `history.cost_usd` is per-review spend, recorded from
  the engine's own report (claude's result event; codex reports none, so those
  rows are 0). It is an API-rate valuation, not money charged, which is also
  the unit `claude.max_budget_usd` is compared against, so the Metrics page's
  median and peak are what a budget should be set from. Usage is account
  headroom; cost is what one review consumed.
- **DuckDB via subprocess.** CGO-free so the binary cross-compiles through the
  family release pipeline. Mirrors `agent-sql`'s driver. Requires the `duckdb`
  CLI at runtime.
- **Config reloads live via getters.** Scheduler, discoverer, and dashboard
  hold `func() config.Config` and re-read per dispatch/sweep/request (each
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
  first-seen, never bumped). A pull that finds nothing ready records nothing
  and simply waits out `schedule.interval`.

- **The queue is consumed continuously, not in batches.** One dispatcher pulls
  the queue LIVE and hands the head candidate to a worker whenever a slot is
  free, waiting `schedule.dispatch_cooldown` between hand-offs. Nothing is
  snapshotted, so a PR that becomes ready mid-review starts on the next free
  slot instead of waiting for the batch. There is no global run-lock:
  `store.Claim` is a compare-and-swap, so two reviewers (even in two
  processes) can never take the same PR, and that is the only exclusion the
  design relies on. Two consequences worth knowing: `run` genuinely competes
  with a live daemon rather than no-opping, and `max_parallel` bounds one
  process, not the store. A candidate that fails BEFORE its claim leaves its
  row untouched, so the dispatcher backs it off — without that it would sit
  at the head being re-offered forever, which the batch loop never had to
  care about. The dispatcher waits in exactly one place, watching the
  completion channel and the idle timer together, which is what lets a
  `max_parallel` raise take effect within one idle poll rather than only
  after some review happens to finish.

- **Steering is untrusted input, and the prompt says so.** A PR's author (or
  the account reviews are posted as) can attach a short instruction that
  shapes the next review of that PR. It is the only part of a prompt written
  by somebody other than the operator, so it renders LAST, inside explicit
  `BEGIN/END STEERING <digest>` markers, under a framing that names the
  setter's ROLE and states what it cannot do. The digest is derived from the
  message, so an author cannot close their own block and continue outside it:
  forging the end marker needs the hash of text they are still writing. The
  message reaches the engine verbatim, markdown included, because mangling it
  is not what makes it safe; the markers are.

  Role matters as much as attribution. Steering from the PR author is framed
  as an interested party; steering from the reviewing account is the operator
  and framed as guidance to weigh. Neither can change the approval policy,
  which is configuration rather than conversation.

- **Who may steer is decided in Go, once, from the store.** The dashboard has
  no login: `tailscale serve` authenticates the person and asserts it in a
  header, and `allowed_authors.tailscale_login` maps that to a GitHub handle,
  which is compared against the queued row's author. Three things must hold
  before the header counts, and only the last is Tailscale's: the daemon is
  not serving over Funnel (public traffic Tailscale attaches no identity to),
  the connection arrived on loopback (so it came through the proxy), and
  Tailscale strips any client-supplied copy. The loopback check is the one
  that does not depend on config being right: a wider bind degrades to
  "nobody is identified" rather than "everybody is whoever they say".

  The answer is computed server-side per queue row (`may_steer`) rather than
  in the client, so the rule exists in one language. The author is always read
  from the store; naming a different one in a request grants nothing.

- **Steering is a queue-row field, not a table.** Same key, same lifetime, one
  per row: as a separate table the 1:1 had to be maintained by hand at every
  site that retires a row, and `Complete` needed an `EXISTS` subquery purely
  to re-derive whether the delete below it was about to fire. A manual add can
  carry steering on the insert, because a freed dispatcher slot claims an
  added row within the idle poll and there is otherwise no window to steer it.
  Discovery re-enqueues every sweep with none attached, so the conflict arms
  KEEP existing steering rather than writing NULL.

- **The scheduler's dependencies are declared, not patched.** `New` takes a
  `Deps` struct: `Store`, `Config` and `Sweeper` are required and everything
  else defaults to its production implementation, so a caller states what it
  cares about and a new seam does not churn every call site. Nothing writes
  a Scheduler field after construction. Single-method dependencies
  (`EngineFactory`, `CandidacyFn`, `LivenessFn`, `UsageFn`, `PriceFn`, the
  clock) are named func types, which is Go's idiomatic shape for one method;
  the interfaces are the ones with a real collaborator behind them,
  `SchedulerStore` and `Sweeper`. There is deliberately no seam that swaps a
  Scheduler method for itself: an object patching its own methods lets a test
  assert the orchestration it supplied rather than the one that ships.

- **The engine is a per-candidate choice, so the usage floor is per engine.**
  A group can name its own engine, model, and effort, so concurrent reviews
  can run both CLIs. Each candidate's policy is resolved ONCE when the
  dispatcher pulls it, alongside the config snapshot it was resolved under,
  and all three travel together on `pending`: the engine build, the headroom
  check, and the prompt must read one answer, never a config that changed
  underneath them. Policy is resolved LAZILY, one candidate at a time until
  one clears its floor, because the dispatcher only ever hands off one and
  resolving the whole queue would cost a DuckDB subprocess per row per pull.
  The floor is an eligibility FILTER: a candidate whose engine is out of
  headroom is never claimed, completed, or recorded, so it waits exactly like
  a cooldown hold and runs when the window refills. That framing is what makes
  it cheap, since the queue already had the vocabulary for "pending but not
  yet actionable". An unbuildable engine is likewise per candidate: one group
  pointing at a broken engine must not stop everyone else's reviews.
- **Every external dependency fails late; diagnose it early.** A missing or
  logged-out engine CLI, an absent duckdb, or a model the permission
  classifier rejects all surface the same way at run time: repeated ERROR
  history rows whose cause sits in the engine transcript. `internal/doctor`
  probes them up front; `serve` runs the same checks at boot and LOGS failures
  rather than refusing to start (the dashboard is still worth serving, and a
  missing CLI may come back). Static config checks that need engine knowledge
  live in `review.Preflight`, not in doctor, so they stay next to the engine
  they describe. The probe set is the REACHABLE engines (the default plus every
  engine a group or override names), not the configured one (a typo in a
  rarely-used group would surface at 3am as an ERROR row) and not every wired
  engine (which would fail a deploy over an engine nothing references);
  Preflight runs per distinct settings combination, because a group's own model
  is exactly what introduces a pairing the base config does not have.

- **An author resolves to a group, and the group IS the policy.** An author
  belongs to one group per repo; the group carries the review level (an ordered
  ladder: `ignore` < `comment` < `approve`), the engine/model/effort, and a
  prompt fragment. That ladder replaced two separate switches that were asking
  one question in two places: `allowed_authors_only_repos` decided whether we
  discovered an author's PRs, and the allow-list decided whether we could
  approve them.

  The split is deliberate and load-bearing. Group DEFINITIONS live in config
  beside the prompts and engine dials they carry; MEMBERSHIP lives in the store
  because it churns and varies per repo. Resolution is pure (`config.Config`
  plus one membership row), so it table-tests without a store, and it builds its
  own trace as it goes: a cascade is only as usable as its explanation, which is
  why `authors who` and `prompts preview --explain` ship with the feature rather
  than after it.

  Two invariants sit ABOVE the cascade and no group or override may touch them:
  you cannot approve your own PR, and an unknown group resolves to `comment`
  (still reviewed, never approved on a policy nobody wrote). `ignore` is a
  DISCOVERY filter, not a veto: a manual `queue add` still reviews, matching
  every other gate manual adds already bypass. See
  `design-docs/decisions/2026-08-author-groups.md`.

- **Nothing environment-specific in code.** Repos, prompts, groups, and cadence
  are config; who is IN each group is per-repo runtime data in the store
  (managed via `authors`). Never hardcode a GitHub handle or repo, not in code,
  docs, or the example config.

- **Transient failures are absorbed at their own boundary, not paid for by a
  long timeout.** Each DuckDB statement is a subprocess taking the file lock
  for its ~25ms life, so a concurrent CLI command can land inside a daemon
  poll; `query` retries a lock conflict (and only a lock conflict) a few times
  over ~300ms rather than surfacing DuckDB's raw error. The pre-review
  candidacy recheck is one `gh` call: it releases its claim on failure so a
  network blip costs the dispatcher's backoff instead of the 2h lease window.
  Engine invocations have their own bounded resume policy (`resumableRun`).
  Discovery, usage and pricing need none of this: each runs on a loop and a
  failed pass is simply retried by the next one.

- **Crash/concurrency safety.** Claims are compare-and-swap leases carrying
  host+pid (`Store.Claim` returns whether you won; losing is a clean skip),
  and boot runs `Scheduler.Reconcile` to release claims left by a dead pid on
  this host, so a mid-review crash never blocks that PR for the lease window.
  Run rows are gone with the batch cycle; the claim is the only lock. `serve` binds the dashboard port before starting any
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
  codex/gh paths. `make test-race` runs the scheduler and CLI under the race
  detector, which is what polices `dispatchState` being lock-free; CI runs it
  too, so a data race there fails the build rather than a comment.
- **Test via injection, not subprocesses.** Extract pure cores and table-test
  them; for effectful code, fake the narrow dependency (embed `store.Store`
  in a struct that overrides only the methods under test, so an unexpected
  call panics loudly). Scheduler tests build through `scheduler.Deps` — the
  same door production uses — rather than writing fields after construction;
  the engine arrives as `NewEngine`, the recheck as `StillCandidate`, the
  sweep as `Sweeper`, the clock as `Now`. Discovery fakes its four-method
  `candidateStore`.
- Errors: `output.New(msg, output.FixableByAgent|Human|Retry)`.
