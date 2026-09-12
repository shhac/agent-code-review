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
│   ├── score.go                # `score ls/show/set/recompute/leaderboard`
│   ├── repos.go                # `repos ls/add/rm`: the watched repos (config)
│   ├── prompts.go              # `prompts show/set/unset/preview`: review prompts
│   ├── configcmd.go            # `config init/path/show/list/get/set/unset`
│   └── usage.go                # top-level LLM reference card
├── config/                     # ~/.config/agent-code-review/config.json + resolved defaults
├── store/                      # Store interface + DuckDB subprocess driver + schema.sql
├── score/                      # author scoring: pure rules, gitattributes matcher, exclusions
│   ├── score.go                # Compute: diff + verdict + revision -> points
│   ├── rules.go                # the resolved ruleset, its hash, and its validator
│   ├── gitattributes.go        # linguist-generated/vendored matching, git's own semantics
│   └── exclude.go              # what counts toward size, after exclusions
├── discover/                   # gh pr list → New/Refreshed/Discussion classification
│   └── diff.go                 # per-file line counts + .gitattributes, GraphQL (never REST)
├── review/                     # Engine interface + codex/claude drivers + prompt/rule assembly
├── scheduler/                  # discovery loop, review dispatcher, parallelism cap, claim leases
│   ├── scheduler.go            # Deps + New: the seam declarations and composition root
│   ├── lifecycle.go            # StartGraceful (daemon) and RunOnce (`run`)
│   ├── dispatch.go             # the consumer loop: pull, hand off, cool down
│   ├── dispatchstate.go        # per-candidate in-flight/backoff bookkeeping
│   ├── loop.go                 # the interval loop (discovery's only)
│   ├── discover.go             # the sweep + its in-flight guard
│   ├── review.go               # reviewOne: claim, recheck, engine, record
│   ├── scoring.go              # fetch the diff at claim time, score after the verdict
│   └── reconcile.go            # release a crashed daemon's claims on this host
├── usage/                      # per-engine subscription-headroom polling + usage-floor predicate
├── doctor/                     # preflight: gh/duckdb/engine binary, auth, and config sanity
├── logbuf/                     # in-memory ring for the daemon's own log tail
└── dashboard/                  # embedded web UI + JSON API over the store
    ├── dashboard.go            # server core + thin read handlers
    ├── queue.go                # queue write surface (add/reorder/remove) + statuses
    ├── reviewlog.go            # /api/review-log: live/postmortem agent-log tail
    ├── stats.go                # /api/stats: last-24h outcome buckets
    ├── leaderboard.go          # /api/leaderboard: author standings (SQL aggregate)
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

- **Author scoring is frozen per review, and the diff is read BEFORE the
  engine runs.** Every completed review earns the PR's author points
  (`internal/score`, pure, no I/O). Two orderings carry the design. First, the
  score is computed once, at completion, and stored with a hash of the ruleset
  that produced it, so retuning a multiplier changes what FUTURE reviews earn
  and nobody loses points they already have; `score recompute` is the
  deliberate act of re-applying new rules to old rows, and it refuses to touch
  all of history without `--all`. Second, the per-file diff fetch happens at
  CLAIM time, not after the verdict. The gap between a verdict and `Complete`
  is microseconds and crash recovery depends on it: put ~31 sequential `gh`
  calls there and a daemon death stops losing a score and starts losing the
  REVIEW, because Reconcile appends an ERROR row, the next claim's recheck sees
  we already reviewed this head on GitHub, and records SKIPPED. Scoring after
  the verdict is therefore pure arithmetic that rides into the same atomic
  history insert.

- **Scoring has three modes, because stopping the work and hiding the results
  are different decisions.** Its only ongoing cost is one GitHub call per
  review to measure the diff, so `leaderboard-only` switches that off while
  still showing the points already earned; `disabled` also hides the page, and
  the nav entry with it. An unrecognised mode reads as ENABLED and is reported
  through doctor rather than silently switching scoring off, which is the
  failure nobody would notice. Routing still matches a hidden page, so
  reaching it by URL explains itself instead of silently redirecting.

- **Review workspaces live in the STATE dir, and are swept.** They used to be
  `os.MkdirTemp("")`, which on macOS is `/var/folders` and gets cleaned by the
  OS: measured on a two-month-old install, 92% of the transcripts history
  pointed at were already gone, so `queue log` and the dashboard's postmortem
  view were empty for almost everything. They are now under
  `xdg.StateDir` (state, not cache: nothing can re-fetch an agent transcript;
  not data: losing one costs a postmortem, not a record), swept at boot against
  `review.workspace_retention`. Owning the location means owning the lifetime,
  and nothing had: the same install held 8,168 directories with no history row
  at all. Rows written before the move keep their dead `/tmp` paths and degrade
  exactly as they already did.

- **Score is proportional to churn, because a flat fee per PR is farmable
  without bound.** The multipliers are a RATE per `churn_unit` lines, not a
  payment for existing. With a flat fee, points tracked how many PRs you opened
  rather than how much was reviewed: a 10-line PR outscored a 1,000-line one
  outright, and 2,000 lines chopped into 200 ten-line PRs scored **1000x** the
  same change shipped whole, with the ratio growing without limit as the change
  got bigger. Scaling by churn caps what any decomposition can gain at the
  spread between the best and worst rates (7.5x shipped), and puts the optimum
  in `small`, which is the behaviour worth paying for. Splitting a 20k PR into
  20 x 1k still earns more than shipping it whole; splitting it into 2,000
  fragments now earns less than either. The original guard test compared ONE
  tiny PR to ONE small PR, which is not the attack, and passed throughout.

- **The size ladder is a curve, not a staircase.** `scoring.curve` reads each
  bucket's `max_churn` either as an ANCHOR the multiplier moves between
  (`linear`, the default, interpolated on log(churn) because the tiers are
  spaced geometrically) or as a flat tier (`step`). Steps are legible but put a
  cliff at every boundary: one line past 1000 churn costs 60% of the rate, and
  adding more tiers only makes more, smaller cliffs. Interpolation takes the
  worst single-line drop from 60% to 0.4% while leaving the farming bound at
  7.5x, because that bound is best rate over worst rate and interpolation moves
  neither end. It does move absolute numbers: a tier's multiplier is now the
  rate at its own boundary rather than across its whole range, so the worked
  examples in internal/score moved with it and only "medium" (250 churn, which
  IS the medium anchor) is unchanged. Repeating a multiplier on two consecutive buckets holds it flat
  between them, so a plateau is expressible and the peak stays a range worth
  aiming at rather than a number worth hitting exactly. The bucket NAME still
  comes from the tier the churn falls in under either curve, because that is
  what makes a score explainable.

- **Two scoring numbers that look arbitrary and are not.**
  `attempt_decay` is validated as strictly under 1, not
  "at most 1": at exactly 1 nothing decays and comment-comment-approve (225)
  outscores a first-pass approval (150), inverting the one ordering the scheme
  exists to enforce. And churn 0 scores 0 rather than falling through to the
  smallest bucket: a PR whose every file is `linguist-generated` (a lockfile
  bump, or this repo's own committed dashboard bundle) would otherwise land in
  `tiny` AND collect the shrink bonus for a net of 0, scoring 120: more than a
  real +200/-100 PR earns. Each is pinned by a test that demonstrates the
  failure rather than just asserting the value.

- **The leaderboard pays once per REVISION, enforced in the aggregate.** Two
  scored history rows at the same `head_sha` would pay twice for one piece of
  work, and that is reachable with no bug in the scoring at all: a review
  outrunning its claim lease can be re-claimed, and if both workers resolve
  their `ScoreContext` before either writes history, neither sees the other and
  both derive a full score. The `Leaderboard` query keeps the earliest scored
  row per `(repo, number, head_sha)`, which closes it in one place rather than
  trying to win a race between processes that may not share a host.

- **The measurement is stored, so policy is re-appliable offline.**
  `history.diff_files` keeps each changed file's path, counts, and the repo's
  own verdict on it (`linguist-generated`/`vendored`, resolved at review time).
  Metadata only, never patch text, capped at 500 files and absent when the
  listing was truncated. It is the same escape-hatch reasoning as `usage_raw`,
  and it is what lets `exclude_paths` and `use_gitattributes` sit INSIDE the
  ruleset hash: a change to either is a `recompute` that re-runs the policy
  over the stored files with no network at all. They were deliberately outside
  the hash until this existed, because flagging rows that nothing could repair
  would have been worse than not flagging them. A change to the REPO's own
  `.gitattributes` is not covered, and should not be: that is the repo changing
  its mind, not us changing our policy.

- **Historical revisions cannot be re-measured, so there is no backfill.**
  Measured against two weeks of real history: of the reviews whose head had
  moved, 5 in 6 had been REBASED rather than merely added to, and a rebased
  PR's old head is orphaned. GitHub will still serve the commit object by SHA,
  but `compare` 404s on it, because a merge base cannot be computed against a
  commit reachable from no ref, and the odds worsen as unreachable objects are
  collected. A partial backfill is also worse than none: only PRs that were
  never force-pushed would score, which silently ranks people by whether they
  rebase. Scoring therefore starts when it is switched on.

- **`refetch` is the only repair for an unmeasured row, and it needs the head
  to still match.** GitHub serves a pull request's file list only at its
  CURRENT head; measuring an older revision means REST `compare`, which bundles
  patch text nobody wants (measured 581KB against this query's 3KB on the same
  PR). So a row whose PR has moved on stays unscored and says why, rather than
  being credited a diff its review never saw. One pipeline does the measuring
  (`discover.Measurer`), shared by completion and refetch, because a second
  copy of it is exactly what produced the earlier drift.

- **Generated files are the repo's declaration, never our list.** Size
  excludes `linguist-generated` / `linguist-vendored` paths read from the
  repo's own `.gitattributes` (the same declaration that collapses them in
  GitHub's diff view), because we do not know which repos this runs against and
  any list we owned would be wrong for all of them. Linguist's BUILT-IN
  heuristics (it knows `package-lock.json` with no config at all) are Ruby and
  are deliberately not reimplemented; `scoring.exclude_paths` is the operator's
  escape hatch until a repo marks its own files. Git's own rules are honoured
  where it counts: EVERY ancestor directory's file applies (skipping them to
  save a few aliases silently counted files a repo had marked one level down),
  last match wins, and `!attr` undoes an earlier rule rather than leaving it
  standing. POSIX bracket expressions are handled too: `[[:digit:]]` contains a
  `]` that closes the inner `[: :]`, and stopping at it produced an invalid
  regexp and dropped the rule in silence. Per-file stats come from
  GraphQL and never REST: `/pulls/{n}/files` returns the full `patch` per file
  with no field selection, measured at 341,081 bytes against this query's 3,032
  on a 13k-line PR.

- **NULL means unscored, which inverts this table's own convention.** Every
  other numeric column on `history` is `NOT NULL DEFAULT 0` under the rule that
  0 means unknown. Scoring cannot follow it, because 0 is a legitimate score, so
  `score` is nullable and aggregates must treat NULL as absent. Relatedly,
  `history` has no primary key and `ReviewLogKey` is a Go-side digest with no
  SQL form, so a score is written against the natural key
  `(repo, number, reviewed_at)` and the write counts what it matched rather
  than trusting it. That is also why history rows are now written with
  `tsExact` (microseconds) rather than `ts` (seconds): the same truncation that
  made the natural key ambiguous had already been silently breaking the history
  pager's tie-break cursor.

- **One derivation, because two of them had already drifted.**
  `store.DeriveScore` is the ONLY place a review becomes points. There are two
  paths that score one (the scheduler at completion, and `score recompute`
  re-deriving later) and they must agree, which a shared arithmetic helper did
  not achieve: the POLICY around the arithmetic was what diverged. The
  scheduler grew a guard against a head that moved mid-review; recompute did
  not, and since the scheduler leaves exactly those rows unscored WITH their
  diff recorded, they were precisely what `--missing` selected, so the recovery
  path scored them off a diff describing code the review never saw. Both guards
  now live inside the derivation, so neither caller can forget one.

- **Absent evidence is not evidence of absence.** A review whose diff fetch
  failed has zeroed counts, and zeroed counts are churn 0, which is a
  legitimate score of nothing for a PR whose every line is generated. Reading
  the two the same way froze a 0 onto real PRs that had merely been rate
  limited, and because the row then LOOKED scored, `--missing` never came back
  for it: the points were gone for good. `DiffStats.Recorded` (a stamped
  `diff_sha`) separates them, and an unfetched row stays NULL and recoverable.
  There is deliberately no `SetReviewDiff`: repairing such a row means
  re-fetching from GitHub, and the setter without that caller was dead code.

- **Attempts count REVISIONS, not reviews.** A `discussion` re-review is a
  second real verdict at the SAME head, so counting verdicts meant replying to
  the bot cost the author a decay step with no new code written. The index is
  the number of distinct earlier head SHAs with a real verdict, and only the
  first verdict per head pays out; later ones record 0.

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
  status column. Likewise "held" is derived (`Held`) from the row's `holds`
  map of name to expiry: the row is reviewable once every one is past
  (`EffectiveReady` = MAX), so holds compose upward and none can undo
  another's deferral. Discovery owns `cooldown` and `settling`; the dashboard
  owns `editing` while an author has the steering editor open. Writes are per
  NAME, which is what stops one writer disturbing another's hold: a sweep
  rewrites its own two, and a manual add clears those same two rather than the
  map. `Promote` (= review now) is the exception and clears everything, floats
  the row, and escalates to manual; drag-reorder never touches holds or
  source. An always-past instant in the same map (`MarkEditingSince`) dates
  the editing session without deferring anything, which is how renewal is
  capped. Queue order is FIFO by first discovery (`discovered_at` is
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
  `BEGIN/END STEERING <nonce>` markers, under a framing that names the setter's
  ROLE and states what it cannot do. The message reaches the engine verbatim,
  markdown included, because mangling it is not what makes it safe; the markers
  are.

  The nonce is RANDOM per rendered prompt, never stored and never shown. It was
  twice derived from the message with SHA-256, which is the wrong shape at any
  length: the function is public and its input is entirely the author's, so
  they can search offline for a message containing the very marker its own
  digest produces, with unlimited attempts and no feedback. At three bytes one
  fell out in five seconds. Randomness removes the search rather than pricing
  it, so there is deliberately no fallback if the system entropy source
  fails — a fallback would be a predictable marker again.

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
