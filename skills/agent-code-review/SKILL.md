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

## Manage allowed authors (whose PRs we may approve — per repo, in DuckDB)

```bash
agent-code-review authors allow owner/name alice --name "Alice" --slack-id U01
agent-code-review authors allow '*' bob            # bob's PRs approvable on every repo
agent-code-review authors ls --repo owner/name
agent-code-review authors deny owner/name alice
```

We are the reviewer: this controls whose PRs WE will approve, not who can
approve. An author listed for a PR's repo (or `*`) may receive an APPROVE;
anyone else is comment-only. Only this PR's author↔allowed pair reaches the
engine.

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
- The agent does the actual review and GitHub actions, then reports back what
  it did (APPROVED|COMMENTED|REQUESTED_CHANGES|SKIPPED). The assembled prompt
  carries a built-in approval directive that defaults to comment-only; approval
  is permitted only for an allowed author's non-self-authored PR. Post-outcome
  behaviour comes from review.on_approve/on_comment/on_reject in config.
- Manage watched repos with `repos ls|add|rm`, prompts with
  `prompts show|set|unset|preview`, and scalar dials with
  `config list|get|set|unset` (all persisted to config.json).
- Every command group has a `usage` subcommand with full docs and examples.
