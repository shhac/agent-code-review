# Author groups: replacing the binary allow-list with a resolved policy

**Date**: 2026-08-06
**Pins**: designed against v0.27.0 (`0f74e38`). Code-internal; nothing external
to pin.
**Status**: designed, not yet built. Supersedes nothing; extends the
allow-list model described in `2026-07-architecture.md`.

All handles and repos below are placeholders.

## The problem

At v0.27.0 an author was one bit: on the allow-list or not. That bit was
consumed in two unrelated places, and the two had drifted into separate
mechanisms for what is really one question.

- `allowed_authors_only_repos` (config, a repo list) decided **whether we
  discover an author's PRs at all**. Repos on the list only enqueued PRs from
  allowed authors; every other repo enqueued anything open.
- The `allowed_authors` table (store, keyed `(repo, github_handle)`) decided
  **whether we may approve**. Off the list meant comment-only, enforced by
  `approvalDirective` and by suppressing the `## If you APPROVED` section.

So the tool could express exactly two treatments (approve-eligible, or
comment-only) plus one repo-wide coarse filter, and could express nothing
about *how* a given author's PRs should be reviewed. Every author got the same
engine, the same model, the same effort, and the same prompt.

What we wanted instead: cohorts. Trusted staff get approvals and a strong
model. Contractors get comments and an explicit-conventions prompt. Bots are
not reviewed unless someone asks. One named individual gets a different model
and a bespoke line in their review posts, on one repo only.

## The model

One idea carries the whole feature:

> Every author resolves to exactly one **group** for a given repo, and a group
> is a complete review policy.

### The ladder

The two existing switches collapse into one ordered enum:

```
ignore  <  comment  <  approve
```

Monotone, one field, and it subsumes both prior mechanisms exactly.
`allowed_authors_only_repos` becomes "unlisted authors on this repo resolve to
a group whose review level is `ignore`". Allow-list membership becomes "this
author resolves to a group whose review level is `approve`".

### Config shape

```jsonc
{
  "authors": {
    // Which group an unlisted author falls into, per repo. "*" is the fallback.
    "unlisted": { "*": "outsider", "acme/infra": "nobody" },

    "groups": {
      "core":     { "review": "approve", "engine": "claude", "model": "opus", "effort": "high" },
      "outsider": { "review": "comment", "prompt": "State our conventions explicitly; link the style guide." },
      "nobody":   { "review": "ignore" }
    },

    // Narrower than a group: patches fields onto whatever group resolved.
    "overrides": [
      {
        "handle": "author-b",
        "repos": ["acme/backend"],
        "model": "claude-opus-5",     // engine inherited from the group
        "effort": "medium",
        "prompt": "Open every post with one sentence addressing them as Lizard Elder."
      }
    ]
  }
}
```

An override is literally a group patch, so it embeds the same struct and every
field is optional in both:

```go
type Group struct {
	Review string // ignore | comment | approve
	Engine string
	Model  string
	Effort string
	Prompt string
}

type AuthorOverride struct {
	Handle string
	Repos  []string // empty = every repo
	Group           // embedded: the same patchable fields
}
```

### Resolution

Two steps, no ambiguity, and pure (no store access), so it table-tests the way
`hold` and `classifyType` already do:

1. **Pick the group**: membership `(repo, handle)` → membership `("*", handle)`
   → `unlisted[repo]` → `unlisted["*"]` → built-in default.
2. **Layer the fields**: group ⊕ each matching override, in config order, field
   by field. Empty inherits.

The output is a resolved `Policy{Group, Review, Engine, Model, Effort, Prompt}`.

Group *definitions* live in config, alongside every other piece of fuzzy,
hand-edited, previewable text. *Membership* stays in DuckDB where the
allow-list already lived: it is roster data, it churns, it varies per repo, and
the dashboard already serves it.

## Decisions, and what was rejected

### Groups carry a full policy inline, not just a label

**Chosen**: a group definition carries the review level, the engine dials, and
a prompt fragment in one block.

**Rejected**: groups as bare labels, with all prompt and engine selection
expressed through `Rules` gated on `groups: [...]`. That had zero overlap with
the existing rules system, but it scattered each cohort across N rule entries,
so no single place answered "what does a contractor get?". Engine selection has
no home in rules anyway.

Rules remain the escape hatch for anything conditional (outcome-scoped,
candidate-type-scoped, repo-combination-scoped). The group's own `prompt` slot
is the unconditional 90% case.

### One group per (repo, author)

**Chosen**: exactly one group per `(repo, handle)`, with the `*` repo as
fallback. Mirrors the existing primary key. An author needing different
treatment per repo gets a different membership row per repo.

**Rejected**: multi-group membership with an ordered merge. More expressive,
but knowing what any one person gets then required reasoning about merge order
and about whether the review level took the strictest or the loosest value.
Per-author overrides cover the cross-cutting cases without that cost.

### `ignore` is a discovery filter, not a hard veto

**Chosen**: authors in an `ignore` group are never auto-discovered, but a
manual `queue add` still reviews them. This matches how manual adds already
bypass every other discovery gate (the candidacy recheck, the quiet period, the
re-review cooldown), so `ignore` needed no new bypass concept.

**Rejected**: a hard veto completing manual adds as SKIPPED. A stronger
guarantee, but it removes the deliberate one-off escape hatch, and it would
have made `ignore` the only policy level enforced in two places.

### The container is a "group"; the resolved thing is a "policy"

**Rejected**: `teams`, because this tool already handles real GitHub team
review requests in discovery (`hasOpenReviewRequest`, and the team-request
failure mode documented in AGENTS.md). Log lines like "author not in team X"
would be genuinely ambiguous against a live entity in the same domain.

**Rejected**: `roles`, for the same reason but weaker (GitHub org roles are
admin / write / triage).

**Rejected**: `policies` as the container name. The definition already has a
`review` field whose value *is* the policy level, and the resolved object is
naturally a policy. Naming the container the same word gives `policy.review`
resolving into a `Policy`, two different things wearing one word. `group.review`
→ `Policy` keeps them distinct, and yields `Facts.Group` + `Facts.Policy`.

Also considered and set aside: `cohorts`, `lanes`, `profiles`, `tiers`,
`bands`, `rings`, `treatments`, `stances`.

## How it lands on the existing machinery

- **Facts and conditions**. `Facts` gains `Group` and `Policy`. `Condition`
  gains `groups: []` and `authors: []`. `author_allowed` survives as an alias
  for `policy == "approve"`, so existing rules, `prompts preview`, and the
  dashboard's preview query params keep working untouched.

- **The self-review veto stays above the cascade**. `canApprove` becomes
  `policy == "approve" && !AuthorIsGHUser`. No group may grant approving your
  own PR; that is not a tunable.

- **Discovery**. The `AuthorScopedRepo` gate becomes `policy != "ignore"`.
  `discover` already holds the store, so the resolver drops straight in.

- **Engine construction moves per candidate**. At v0.27.0 the engine was built
  once per cycle in `reviewCycle` and handed down to `processQueue`. It becomes
  a factory, with `reviewOne` building its own. The dials reach the drivers via
  a patched `ReviewSettings` clone, so no driver learns that groups exist.
  `Provenance` already recorded model and effort per row, so history stays
  truthful once a single cycle can mix engines.

- **Store migration**. `ALTER TABLE allowed_authors ADD COLUMN IF NOT EXISTS
  group_name TEXT`, in the same idempotent block as every other migration,
  backfilled to a built-in approve group. Existing rows keep their exact
  current behaviour with no user action.

- **Introspection is part of the feature, not a follow-up**. In a cascade, the
  resolution trace is what keeps the config knowable. `prompts preview
  --explain` should print the layer that decided each field, e.g.
  `group=core via membership(*); model=claude-opus-5 via override[author-b]`.

## The usage floor with mixed engines

Once one cycle can run both engines, the v0.27.0 arrangement is wrong: the
floor gated the *whole cycle* on the *configured* engine's snapshot.

The decided behaviour:

> If one engine is at its floor and the other is not, candidates bound for the
> floored engine wait; candidates bound for the engine with headroom run as
> normal.

The parts were mostly there already. `usage.Cache` was keyed by engine and
`serve` already polled every engine (so the dashboard could show both). Only
the single `usageFn()` handed to the scheduler collapsed that back to one
snapshot.

- `UsageFn` becomes `func(engine string) usage.Snapshot`.
- The floor stops being a cycle gate and becomes an **eligibility filter**,
  sitting alongside the existing `eligible_at` hold in `availableCandidates`. A
  floored candidate is simply not claimed this cycle. It is never completed and
  never recorded, so it resumes the moment the window refills, exactly like a
  cooldown hold.
- Modelling it as a hold rather than a skip is what keeps this cheap: the queue
  already has the vocabulary for "pending but not yet actionable".
- One property to preserve deliberately: at v0.27.0 the floor was checked
  *before* the run-lock, so a paused cycle recorded no run and the runs table
  stayed free of empty ticks. With a per-candidate floor, the cycle must still
  short-circuit before the run-lock when *every* available candidate is
  floored. Resolving policy needs only config plus store membership, so this is
  computable at that point.
- The transient hold is worth surfacing in the dashboard next to the persisted
  `hold_reason` values, so "why is nothing moving" stays answerable.

## Doctor and boot validation

A smaller grow than it first appears. `usageSources` already iterated every
wired engine, and `BinFor(engine)` already existed for callers that diagnose
all of them rather than just the active one. What changes is the *set*:

- Not "the configured engine" (too narrow: a typo in a rarely-used group would
  first surface at 3am as an ERROR row buried in a transcript).
- Not "every wired engine" (too broad: it would fail a deployment over an
  engine no group references).
- **The reachable set**: the default engine, plus every distinct engine named
  by any group or override. Each check should name which groups depend on it.

`review.Preflight` is currently called once on `cfg.Review`. It becomes one
call per distinct resolved settings combination in the reachable set, because
its checks are model-and-mode specific (the auto-mode model compatibility trap)
and a group is exactly the thing that can introduce a bad pairing.

## Build order

1. Config schema, resolver, and tests. Pure; nothing else moves.
2. Store migration, membership CRUD, `authors group set/rm/ls`.
3. Facts / Condition / prompt wiring, plus the preview resolution trace.
4. Per-candidate engine factory; per-engine usage floor as an eligibility
   filter; doctor and preflight over the reachable set.
5. Dashboard read surface; AGENTS.md.

Steps 1 through 3 are shippable without step 4: groups that only differ in
review level and prompt work with a single cycle-wide engine. Step 4 is what
makes per-group engine dials real.

## Deliberately not done

- **No per-repo group definitions.** The repo dimension lives in membership and
  in `override.repos`. A group meaning different things on different repos
  would reintroduce the ambiguity that one-group-per-repo was chosen to avoid.
- **No group inheritance between groups.** Two layers of cascade (group, then
  override) is already the most a reader can hold. A third would make the
  resolution trace mandatory reading rather than a debugging aid.
- **`ignore` does not stop a review already in flight.** It is evaluated at
  discovery and at claim time, not mid-run.
