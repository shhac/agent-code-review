---
name: agent-code-review
description: |
  PR review queue and scheduler CLI. Use when inspecting or managing the
  queue of pull requests awaiting automated review, adding, removing,
  promoting, or skipping candidates by hand, running a one-shot review pass
  or the serve daemon and its dashboard, or checking which repos, author
  groups, and schedule the reviewer is configured with. Also covers author
  SCORING: the points a reviewed PR earns its author, the leaderboard
  standings, and correcting or re-deriving a score. Triggers: unblock PRs,
  leaderboard, who is winning, why did this PR score that.
allowed-tools: Bash(agent-code-review *) Read Grep Glob
---

# PR review queue with `agent-code-review`

`agent-code-review` is a CLI binary on `$PATH`. Default output is **NDJSON**:
one JSON record per line on stdout. Errors go to stderr as one JSON line
`{"error": "...", "fixable_by": "agent"|"human"|"retry", "hint": "..."}` with a
non-zero exit.

It maintains a DuckDB-backed queue of candidate PRs and reviews them with a
pluggable engine (Codex or Claude Code; default: Codex). Configuration lives at
`~/.config/agent-code-review/config.json`: repos, the author groups, age
thresholds, schedule, and the review prompt + rules.

## Inspect the queue

```bash
agent-code-review queue ls                   # all pending candidates, NDJSON
agent-code-review queue ls --repo owner/name
```

The queue holds only pending work, FIFO by first discovery; a row with
`claimed_at` set is being reviewed right now, and a row carries a `holds` map
of name to expiry (`settling` = the PR was pushed or edited within
`candidates.quiet_period`; `cooldown` = we reviewed it within
`candidates.rereview_cooldown`; `editing` = its author has the dashboard's
steering editor open, for `candidates.steering_hold`). It is dispatched once
every one of them is past, keeping its queue position and being stepped over
until then. Completed
outcomes live in history (see the dashboard's History page).

## Manage candidates

```bash
agent-code-review queue add     owner/name 1234   # add a PR (fetches live metadata; rejects closed/merged; no holds)
agent-code-review queue promote owner/name 1234   # review NOW: top of queue, clears any hold, treated as manual
agent-code-review queue skip    owner/name 1234   # record SKIPPED and drop (re-eligible on new commits)
agent-code-review queue rm      owner/name 1234   # remove, recording nothing
agent-code-review queue log     owner/name 1234 -f # stream the review agent's log (live or postmortem)
```

## Manage the author roster (which group each author is in, per repo, in DuckDB)

```bash
agent-code-review authors set owner/name alice core --name "Alice" --slack-id U01
agent-code-review authors set '*' bob outsider     # that group on every repo
agent-code-review authors ls --repo owner/name     # rows + the policy each resolves to
agent-code-review authors groups                   # the cohorts and what each grants
agent-code-review authors who alice --repo owner/name
agent-code-review authors rm owner/name alice
```

We are the reviewer. An author belongs to ONE group per repo, and the group
(defined in config under `authors.groups`) is a complete review policy: the
review level (`ignore` = never discovered, though a manual `queue add` still
reviews; `comment` = reviewed but never approved; `approve` = approvable), plus
the engine, model, effort, and an extra prompt fragment. `authors.overrides`
narrows any of that per handle.

Resolution: the roster row for this repo, else the row for `*`, else
`authors.unlisted[repo]`, else `authors.unlisted["*"]`; then every matching
override patches it field by field. `authors who` names the deciding layer per
field, which is how to answer "why did that PR get approved / ignored". Only
this PR's own resolved policy reaches the engine, never the roster.

## Author scores and the leaderboard

Every completed review earns the PR's **author** points, from the diff's size,
the verdict, whether the codebase grew or shrank, and how many revisions it
took. Generated and vendored files are excluded, read from the repo's own
`.gitattributes`.

```bash
agent-code-review score leaderboard                  # standings, highest first
agent-code-review score leaderboard --days 30        # a window
agent-code-review score show owner/repo 123          # every scored review of one PR
agent-code-review score ls --missing                 # rows that were never scored
```

Points scale with how much was reviewed, so the bucket multipliers set a RATE
rather than a flat fee per PR. Splitting a large change into well-sized pieces
earns more than shipping it whole; fragmenting it into tiny ones earns less
than either.

Scores are **frozen** when a review completes, alongside a hash of the rules
that produced them, so retuning config changes what FUTURE reviews earn and
nobody loses points they already have.

```bash
agent-code-review score ls --stale                   # scored under older rules
agent-code-review score recompute --stale --dry-run  # what would change
agent-code-review score recompute --stale            # apply it
agent-code-review score refetch --missing            # re-measure from GitHub
agent-code-review score set owner/repo 123 0 --note "duplicate of #120"
```

`recompute` is offline: it re-derives from each row's stored measurement, so
changing which paths are excluded needs no network. `refetch` asks GitHub again
and is the only repair for a row whose size was never measured; it needs the PR
to still be at the head that was reviewed.

Both move points people already have, so both refuse to touch all of history
without `--all`, both take `--dry-run`, and a score set by hand is left alone
unless `--include-manual` is passed.

## Run reviews

```bash
agent-code-review run                                # drain the queue, then exit
agent-code-review serve --http :8330                 # daemon + dashboard
agent-code-review serve --http :8330 --tailscale serve   # + expose on tailnet
```

## Configuration

```bash
agent-code-review config path      # where the config lives
agent-code-review config show      # current config (NDJSON)
```

See `config.example.json` in the repo for the full shape. The CLI never
hardcodes repos or GitHub handles; everything is config.

## Notes

- Requires `gh` (authenticated), the `duckdb` CLI, and the configured review
  engine (`codex` by default, or `claude`) on `$PATH`, already authenticated.
- Candidate rules: **NEW** (never reviewed, ≤14d) and **REFRESHED** (head SHA
  changed since our last review, ≤21d). Processed FIFO by first discovery, up
  to 4 in parallel. Already-approved PRs are skipped, and any recorded outcome
  at the current head SHA suppresses re-enqueueing.
- Discovered candidates wait out eligibility holds (quiet period default 15m,
  re-review cooldown default 90m, `0s` disables) so the agent doesn't review
  mid-push or instantly re-review; `queue promote` or a manual `queue add`
  bypasses them. Manual rows also skip the pre-review candidacy recheck.
- Most config edits reload live within ~30s (cadence, parallelism, usage
  floors, repos, prompts); only the loop switches and dashboard/Tailscale
  settings need a daemon restart. A candidate is HELD when the engine that
  would review it drops below `schedule.usage_floor.*` percent remaining. The
  floor is per engine, since a group can name its own: one engine being out of
  headroom does not hold candidates bound for the other. A held candidate is
  never claimed or recorded, so it runs when the window refills.
- The agent does the actual review and GitHub actions, then reports back what
  it did (APPROVED|COMMENTED|REQUESTED_CHANGES|SKIPPED). The assembled prompt
  carries a built-in approval directive that defaults to comment-only; approval
  needs an `approve`-level group AND a non-self-authored PR (the self-review
  veto sits above the group cascade and no group can lift it). Post-outcome
  behaviour comes from review.on_approve/on_comment/on_reject in config.
- Manage watched repos with `repos ls|add|rm`, prompts with
  `prompts show|set|unset|preview`, and scalar dials with
  `config list|get|set|unset` (all persisted to config.json).
- Scoring can be switched off globally with `config set scoring.mode`:
  `enabled` (measure and score), `leaderboard-only` (stop measuring, which
  removes the per-review GitHub call scoring adds, while still showing the
  points already earned), or `disabled` (also hides the leaderboard in the
  dashboard). It can be narrowed per repo under `scoring.repos`.
- A second review at the same commit (somebody replying to the bot) scores 0:
  it is discussion, not new work. The leaderboard pays once per revision.
- Every command group has a `usage` subcommand with full docs and examples.
