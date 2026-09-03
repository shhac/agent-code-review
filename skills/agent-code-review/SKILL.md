---
name: agent-code-review
description: |
  PR review queue and scheduler CLI. Use when inspecting or managing the
  queue of pull requests awaiting automated review, adding, removing,
  promoting, or skipping candidates by hand, running a one-shot review pass
  or the serve daemon and its dashboard, or checking which repos, author
  groups, and schedule the reviewer is configured with. Triggers: unblock
  PRs.
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
`claimed_at` set is being reviewed right now, and a row with `eligible_at` in
the future is **on hold** (`hold_reason`: `settling` = the PR was pushed or
edited within `candidates.quiet_period`; `cooldown` = we reviewed it within
`candidates.rereview_cooldown`); it is not dispatched until then. Completed
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
- Every command group has a `usage` subcommand with full docs and examples.
