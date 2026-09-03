# Replacing the review cycle with a continuous consumer pool

**Date**: 2026-09-03
**Pins**: built on `8120434` (after skill-v0.31.2). Code-internal; nothing external
to pin.
**Status**: built. Supersedes the run-lock and per-cycle framing described in
`2026-07-architecture.md` and `2026-08-author-groups.md`.

All handles and repos below are placeholders.

## The problem

Reviews ran in batches. Each tick of the review loop took a snapshot of the
queue, filtered it, and processed that fixed list up to `schedule.max_parallel`.
Within a batch a freed slot refilled immediately, so this looked fine on a
quiet queue. It was not fine on a busy one, because two things combined:

1. The candidate list was frozen at the top of the cycle. Nothing joined it.
2. An advisory run-lock (a `running` row in a `runs` table) made every
   subsequent tick a no-op until that cycle finished.

With four slots and one twenty-minute review in flight, three slots sat idle
for twenty minutes. Work that was ready the whole time could not start: a PR
discovered mid-cycle, a settling hold that expired, an engine that climbed
back over its usage floor. The queue was being consumed in lockstep with its
slowest member.

## What we chose

One dispatcher goroutine, N workers. The dispatcher pulls the queue live, hands
the head candidate to a worker whenever a slot is free, and waits
`schedule.dispatch_cooldown` (new, default 5s) between hand-offs. Nothing is
snapshotted.

A single puller rather than N workers each racing the store, because the pull
does not mark the row — the worker's claim does, later. Two independent pullers
would routinely hand the same candidate to two workers, and the loser would
burn a slot and a temp dir discovering that. One puller with an in-flight set
makes that impossible in-process; the claim CAS remains the cross-process
guard.

`schedule.interval` kept its name and changed meaning, from batch cadence to
idle poll. A rename would have been a config break for a dial most users have
set, and `LeaseWindow` still derives from it.

## What the run-lock was actually doing

The tempting argument was that the run-lock was redundant: `store.Claim` is a
compare-and-swap (`UPDATE ... WHERE claimed_at IS NULL OR claimed_at < stale
RETURNING 1`), so two daemons sharing a store already could not review the same
PR. True, and not the whole story. Removing it needed three other jobs
re-homed, none of which were written down anywhere:

**A store-wide spend cap.** `max_parallel` bounds one process. "One cycle at a
time, store-wide" was the only thing bounding the total. Accepted as lost: the
dashboard port bind still permits one daemon per address, which is the
realistic deployment. Daemons on two hosts against one store can now exceed
`max_parallel` in aggregate. Correct, just not capped.

**The usage floor against `run`.** `run` passed a nil usage getter on purpose:
a manual kick was meant to bypass the floor. That was survivable only because
the run-lock made a cron `run` overlapping a live daemon a fast no-op. Without
it, a scheduled run would drain the queue with the floor disabled at exactly
the moment the daemon had parked itself at that floor. Fixed by giving `run`
the floor (a memoized one-shot probe per engine, since it exits), with
`--ignore-usage-floor` to opt back out.

**DuckDB write headroom.** The store shells out to `duckdb` per query under a
global mutex, with no retry or backoff; a lock conflict surfaces as a raw error
string. The old `resolvePending` resolved every candidate's author policy up
front. At batch cadence that was once a minute; at dispatch cadence it would be
a subprocess per queue row per pull. Fixed by resolving lazily, one candidate
at a time, stopping at the first that clears its engine's floor — which is all
the dispatcher needs, since it hands off exactly one.

## What the batch loop was absorbing for free

**Poison-pill starvation.** Two paths fail *before* the claim and leave the
queue row untouched: an unbuildable engine (a group naming an engine whose
binary is gone) and a workdir that cannot be created. The cycle walked a
snapshot, so a candidate like that cost one slot and the rest of the batch ran.
A dispatcher that always offers the head would re-offer it forever, starving
everything behind it — and with `dispatch_cooldown` at `0s`, spinning hot.
Failures now back off per candidate, doubling from a minute to an hour.

The same applies to a roster lookup that errors. The old code failed closed by
aborting the whole cycle, on the principle that guessing a policy is how a PR
gets approved that shouldn't be. The principle survives; the blast radius does
not. The candidate is skipped and backed off, and the healthy ones run.

**Shutdown ordering.** A select with a ready case chooses uniformly at random,
so the batch loop checked `gracefulCtx.Err()` *before* its select, or it would
start a review roughly half the time after a shutdown was requested. The
dispatcher has the same hazard in a new place, plus two waits (idle poll,
cooldown) that must be context-aware or the first Ctrl-C is left up to a full
interval behind.

## What went with the cycle

The `runs` table, `/api/runs`, the "Recent runs" panel and the "last run" tile.
A run stopped being a thing that happens, and everything those panels
summarised is on History already, per review, with more detail. Fresh stores no
longer create the table; existing ones keep it and its rows, unread, rather
than losing the history to a migration.

## Alternatives considered

**Live refill inside the cycle.** Keep the run row and the cycle boundary, but
have the batch loop re-list the queue each time a slot frees. Much smaller
diff, and it fixes the same user-visible problem. Rejected because the cycle
would then rarely end — a continuously fed queue means a run row open for
hours, which `ActiveRun` would eventually judge stale and let a second cycle
start alongside the first. The run row would have needed a heartbeat to keep a
concept we no longer wanted.

**A daemon heartbeat row.** Replace the per-cycle run row with a single
liveness row, so `run` still no-ops while `serve` holds it. This preserves the
store-wide spend cap that we accepted losing. Rejected as reintroducing a
smaller version of the mechanism being removed, for a multi-host case we do not
have. Worth revisiting if daemons on two hosts ever share a store.
