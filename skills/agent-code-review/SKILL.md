---
name: agent-code-review
description: |
  PR review queue + scheduler CLI. Use when:
  - Inspecting or managing the queue of PRs awaiting automated review
  - Adding/removing/promoting/skipping candidate PRs by hand
  - Running a single review cycle or the serve daemon + dashboard
  - Checking the review configuration (repos, allow-list, schedule)
  Triggers: "code review queue", "pr review queue", "agent-code-review", "review candidates", "review dashboard", "unblock PRs"
allowed-tools: Bash(agent-code-review *) Read Grep Glob
---

# PR review queue with `agent-code-review`

`agent-code-review` is a CLI binary on `$PATH`. Default output is **NDJSON** —
one JSON record per line on stdout. Errors go to stderr as one JSON line
`{"error": "...", "fixable_by": "agent"|"human"|"retry", "hint": "..."}` with a
non-zero exit.

It maintains a DuckDB-backed queue of candidate PRs and reviews them with a
pluggable engine (default: Codex). Configuration lives at
`~/.config/agent-code-review/config.json` — repos, the approval allow-list, age
thresholds, schedule, and the review prompt + rules.

## Inspect the queue

```bash
agent-code-review queue ls                 # all candidates, NDJSON
agent-code-review queue ls --status queued # only those awaiting review
agent-code-review queue ls --repo owner/name
```

## Manage candidates

```bash
agent-code-review queue add     owner/name 1234   # add a PR
agent-code-review queue promote owner/name 1234   # float to the top
agent-code-review queue skip    owner/name 1234   # skip this cycle
agent-code-review queue rm      owner/name 1234   # remove
```

## Manage the approver allow-list (per repo, in DuckDB)

```bash
agent-code-review approvers add owner/name alice --name "Alice" --slack-id U01
agent-code-review approvers add '*' bob            # approvable on every repo
agent-code-review approvers ls --repo owner/name
agent-code-review approvers rm owner/name alice
```

A PR author listed for its repo (or `*`) may be approved; anyone else is
comment-only. Only this PR's author↔approvable pair reaches the engine.

## Run reviews

```bash
agent-code-review run --once                         # one cycle, then exit
agent-code-review serve --http :8330                 # daemon + dashboard
agent-code-review serve --http :8330 --tailscale serve   # + expose on tailnet
```

## Configuration

```bash
agent-code-review config path      # where the config lives
agent-code-review config show      # current config (NDJSON)
```

See `config.example.json` in the repo for the full shape. The CLI never
hardcodes repos or GitHub handles — everything is config.

## Notes

- Requires `gh` (authenticated), the `duckdb` CLI, and `codex` on `$PATH`.
- Candidate rules: **NEW** (never reviewed, ≤14d) and **REFRESHED** (head SHA
  changed since our last review, ≤21d). Processed New-first, oldest-first, up to
  4 in parallel.
- The engine does the actual review and any GitHub/Slack actions; comment-only
  behaviour for self-authored / non-approvable PRs is enforced via prompt rules.
