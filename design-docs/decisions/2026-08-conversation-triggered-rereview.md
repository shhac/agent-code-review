# Conversation-triggered re-review: splitting the dedup fingerprint

**Date**: 2026-08-27
**Pins**: designed against `agent-code-review` v0.30.2 (`5dcc241`) and the
`pr-issue-review` skill at `affd6a1`. External surface pinned: `gh` 2.98.0.
**Status**: designed, not yet built. Extends the deduplication model in
`references/diff-equivalence.md` (skill side) and the New/Refreshed
classification described in `2026-07-architecture.md` (scheduler side).

All handles, repos, and SHAs below are placeholders.

## What was found

A PR was reviewed, the reviewer left one `P1`, and the author responded by
arguing the finding was wrong and editing the PR description to match. No code
was pushed. The next two scheduled runs both recorded `SKIPPED`, roughly 45
seconds and 220k tokens each, and said nothing. Thirty-five minutes later a
human reviewer raised two blocking findings, one of which contradicted a
conclusion our review had explicitly cleared. Only when the author finally
pushed a commit did the SHA move and a real review happen.

The skipped run's own final output named the rule it hit: this exact head SHA
already had a review from this skill at this profile, so deduplication
prohibited another.

That was correct per the rules as written. The rules were wrong.

## Three independent gaps

**1. The context fingerprint was unreachable in the only case it existed for.**

Exact head-SHA deduplication ran *before* the diff-equivalence check, and
stopped the run unconditionally. But the context fingerprint was written to
catch "same code, changed conversation or acceptance criteria" — which is
precisely the case where the SHA has *not* moved. It could only ever be
consulted after the SHA gate let a run through, i.e. only on a rebase or
force-push. It was dead code for its own purpose.

The fingerprint would have fired here: reconstructing the payload at the time
of the first skip produced a different hash from the one stamped on the
previous review. Nothing consulted it.

**2. Even if reached, it could not see the reply.**

The context recipe read `gh pr view --json ...,comments,reviews`. `comments`
returns issue comments only; `reviews` returns review *bodies*. An inline reply
on a review thread appears in neither — its enclosing review carries an empty
body. Checking the assembled payload for the author's rebuttal text returned
zero matches. Every inline comment on the PR was invisible.

The entire "author argues back on a finding" channel — the highest-signal
context for a re-review — sat outside the fingerprint.

**3. Nothing could force a rerun.**

The skill's escape hatch was "unless explicitly rerun". The scheduler's
assembled prompt carried only `- Type: new|refreshed`. There was no rerun
signal on the wire at all. A manual queue add bypassed discovery filters and
eligibility holds, then hit the skill's head-SHA rule and died anyway.

## The design

### Three fingerprints, not one

The stamp already carried two hashes (`diff` and `context`). `context`
conflated two channels that want different rules. Split into three:

| name | source | moves when |
| --- | --- | --- |
| `diff` | PR patch through `git patch-id --stable` | code changes |
| `intent` | title, body, base ref, closing issue refs | the stated claim changes |
| `convo` | human discussion, incl. review threads | a person says something |

The value of the split is not tidiness. It is that each channel gets its own
projection (so noise can be filtered per channel) and its own consequence (so a
reply can earn a cheaper review than a force-push does).

### Channel policy

| signal | `intent` | `convo` | note |
| --- | --- | --- | --- |
| body or title edit | yes | — | acceptance criteria moved |
| base branch retarget | yes | — | |
| human top-level comment | — | yes | |
| human review body | — | yes | |
| human inline reply | — | yes | the gap that caused this |
| thread resolved, no reply | — | yes | "I've handled it" |
| bot comment | — | no | |
| bot comment edited in place | — | no | moved the old hash on every redeploy |
| emoji reaction | — | no | already excluded by projection |
| our own review or inline comment | no | no | see loop hazard |
| new commit | — | — | `diff` |
| rebase onto moved base | no | no | absorbed by `patch-id` |

The principle that settles "but the bot's comment is useful context": **a
fingerprint is a wake-up trigger, not a context inventory.** Bot comments are
still read during a review. They just do not cause one.

### Exclusion is per comment, not per thread

The obvious implementation — drop threads we started — destroys the signal.
The canonical shape of a useful re-review trigger is our finding at position 0
and the author's rebuttal at position 1. Filtering by thread origin discards it.

But naive per-comment filtering reintroduces the loop: posting a new inline
comment adds a thread to the array, which moves the hash, which triggers a
review, which posts a comment. The rule that satisfies both:

> Include a thread if it has at least one surviving human comment **or**
> `isResolved` is true. Hash only the surviving comments, plus the resolved flag.

| thread state | included | why |
| --- | --- | --- |
| ours, no reply | no | no self-trigger when we post |
| ours + their reply | yes (reply only) | the signal being built for |
| ours, silently resolved | yes (flag, no bodies) | the silent-resolve signal |
| bot's, no human reply | no | |
| bot's + their reply | yes (reply only) | |

Two constraints follow, and both must hold or the loop returns:

- Inline comments need a hidden marker of their own. The top-level marker is
  the lizard prefix; inline comments carried nothing. Bot-typename filtering
  does **not** help here: the account running the skill is a normal user
  account and reports as a `User`, not a `Bot`. The marker is load-bearing and
  must ship before or with `convo`, never after.
- The skill must not resolve threads. If it ever gains that ability, resolving
  its own thread self-triggers.

### Data sources

`reviewThreads` is not a `gh pr view` field, so this channel needs GraphQL
regardless; that is also the only place carrying resolution state. Author
type comes back as a typename, which cleanly separates bots from humans.

Nested pagination is the awkward part: threads paginate, and comments paginate
*within* each thread. Silent truncation would freeze the hash and lose a
trigger, which is the exact failure being fixed. So: **if any page cursor
reports more pages, emit `convo=unknown`.** `unknown` never matches, so the run
falls open into a review. That matches the existing semantics for `diff` and
`context` and needs no new concept.

### Consequence per channel

This is the payoff a single hash cannot express.

- `diff` moved: full review. New code.
- `intent` moved: full review. The claim changed, so every prior finding needs
  re-judging against new criteria.
- `convo` moved alone: **targeted re-review.** Load the previous review's
  findings, judge each against what was said, post a short follow-up that
  confirms, withdraws, or holds. Do not re-derive the PR.

On the PR that prompted this, the correct action at the first skip was "the
author disputed your P1 with a concrete claim about what the old menu group
contained; go check it and withdraw if he is right" — not another cold pass.
Measured against the runs that did happen, that is roughly 40s and 200k tokens
against 230s and 1.1M, and it is closer to what a human reviewer does when the
author replies.

### Stamp format

`v1` becomes `v2`: `profile= head= diff= intent= convo=`.

A `v1` stamp has no `intent` or `convo`. Both read as `unknown`, so a `v1`
stamp never permits a skip. Cost is one extra review per open PR at rollout,
self-healing after. No migration, no back-compat parsing beyond recognising
the older prefix.

## Scheduler side

The scheduler stays coarse and timestamp-based. Content identity belongs to the
skill; the scheduler should not be recomputing hashes.

- Same-SHA suppression currently returns early on any recorded outcome at the
  PR's current head SHA. Widen it: same SHA suppresses only when there has been
  no human conversation activity since the recorded outcome's timestamp, taken
  as the newest `created_at` across human issue comments, review comments, and
  reviews.
- Add a third candidate type, `discussion`, alongside `new` and `refreshed`.
  The candidate-type enum is a two-element list today and prompt rules already
  gate on candidate type, so the "the author replied; revisit your open
  findings" instruction drops into a conditioned prompt slot without the
  scheduler knowing anything about review semantics.
- Add `candidates.discussion_max_age_days`, mirroring the existing
  `new_max_age_days` (14) and `refreshed_max_age_days` (21). Suggested default
  14: conversation on a three-week-old PR is usually about landing it, not
  about the review.
- The existing re-review cooldown covers bursts. Two replies seconds apart
  must not fire two runs.

The scheduler will over-trigger slightly — a human "lgtm" wakes it. That is
acceptable when what it wakes into is a targeted pass rather than a full one.

## Alternatives considered

**Widen the single `context` hash to include review threads.** Cheapest change,
and it fixes gap 2. Rejected: it cannot express per-channel noise policy (bot
comments edited in place would still buy full re-reviews), and it cannot give a
reply a different consequence from a body edit. The reply case is the common
one and deserves the cheap pass.

**Four fingerprints, splitting top-level comments from thread replies.**
Rejected as over-fine: both produce the same consequence, so the extra hash
buys only diagnosis, which the three-way split already provides.

**Scheduler-side content hashing instead of timestamps.** Rejected: duplicates
the skill's fingerprint logic in Go, in a second place that can drift, to save
an occasional cheap targeted pass.

## Risks

- **Self-trigger loop** if the inline marker ships after `convo`. Sequence it
  first. This is the one ordering constraint in the plan.
- **GraphQL pagination** on long-lived PRs. Mitigated by failing open to
  `unknown`, which costs a review rather than losing one.
- **Targeted re-review is a new output shape** in the skill, with its own
  contract and voice. It is the only genuinely new machinery here; the rest is
  reordering and projection changes.
- **Over-triggering on social comments.** Accepted, bounded by the max age and
  the cooldown.
