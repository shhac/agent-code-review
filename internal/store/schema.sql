-- Persistent work queue + outcome history for agent-code-review.
-- Applied idempotently on every Store.Init.

-- The work queue: a row exists if and only if the PR has pending review work.
-- The primary key IS the "same PR queued once" guarantee, and completion
-- removes the row (atomically with its history insert) — there is no status
-- column to go stale.
CREATE TABLE IF NOT EXISTS queue (
  repo          TEXT    NOT NULL,
  number        INTEGER NOT NULL,
  type          TEXT    NOT NULL,               -- 'new' | 'refreshed'
  title         TEXT,
  author        TEXT,
  url           TEXT,
  head_sha      TEXT,
  created_at    TIMESTAMP,
  updated_at    TIMESTAMP,
  queue_pos     INTEGER,
  discovered_at TIMESTAMP,                      -- first time discovery saw this pending work; NEVER bumped by later sweeps
  claimed_at    TIMESTAMP,                      -- set while an engine reviews it; NULL = unclaimed. Stale claims (crashed daemon) are reclaimed by the next cycle.
  claim_host    TEXT,                           -- which daemon holds the claim (host + pid) — lets a rebooted daemon clear its own dead claims immediately instead of waiting out the lease
  claim_pid     INTEGER,
  source        TEXT NOT NULL DEFAULT 'discovered', -- 'discovered' | 'manual'. Manual adds bypass the pre-review candidacy check (drafts and explicit re-review requests must go through).
  work_dir      TEXT,                           -- the engine's scratch workspace, set at claim time; its agent.log is the live review log
  eligible_at   TIMESTAMP,                      -- eligibility hold: the scheduler skips this row until then. NULL = eligible now. Manual adds/promotion clear it.
  hold_reason   TEXT,                           -- why the hold exists: 'cooldown' (recently reviewed by us) | 'settling' (PR updated too recently)
  PRIMARY KEY (repo, number)
);

-- Append-only outcome history: one row per completed queue item, including
-- SKIPPED and ERROR outcomes. Duplicates per (repo, number) are expected —
-- the same PR can be reviewed many times. The most recent REAL verdict
-- (APPROVED|COMMENTED|REQUESTED_CHANGES) per PR drives Refreshed detection;
-- the most recent row of ANY verdict at the PR's current head SHA suppresses
-- re-enqueue.
CREATE TABLE IF NOT EXISTS history (
  repo          TEXT      NOT NULL,
  number        INTEGER   NOT NULL,
  title         TEXT,                           -- PR title at completion time, for display
  author        TEXT,                           -- PR author at completion time, for display
  head_sha      TEXT      NOT NULL,
  verdict       TEXT      NOT NULL,             -- APPROVED|COMMENTED|REQUESTED_CHANGES|SKIPPED|ERROR
  engine        TEXT,
  reviewed_at   TIMESTAMP NOT NULL,
  duration_secs INTEGER   NOT NULL DEFAULT 0,   -- claim-to-completion elapsed; 0 for rows predating the column and for manual skips
  work_dir      TEXT,                           -- the engine workspace used, kept for postmortem log access
  tokens_used   INTEGER   NOT NULL DEFAULT 0    -- engine-reported token spend; 0 when unknown
);

-- Idempotent migrations for stores created before these columns existed.
-- Init applies this whole file on every boot; these are no-ops once applied.
-- (No NOT NULL here: DuckDB can't add constrained columns; DEFAULT 0
-- backfills the pre-existing rows, and Complete always writes a value.)
ALTER TABLE queue ADD COLUMN IF NOT EXISTS work_dir TEXT;
ALTER TABLE queue ADD COLUMN IF NOT EXISTS eligible_at TIMESTAMP;
ALTER TABLE queue ADD COLUMN IF NOT EXISTS hold_reason TEXT;
ALTER TABLE queue ADD COLUMN IF NOT EXISTS claim_host TEXT;
ALTER TABLE queue ADD COLUMN IF NOT EXISTS claim_pid INTEGER;
ALTER TABLE history ADD COLUMN IF NOT EXISTS duration_secs INTEGER DEFAULT 0;
ALTER TABLE history ADD COLUMN IF NOT EXISTS work_dir TEXT;
ALTER TABLE history ADD COLUMN IF NOT EXISTS tokens_used INTEGER DEFAULT 0;

-- Per-repo allowed authors: whose PRs WE (the reviewer) may approve — not who
-- can approve. A PR may receive an APPROVE only when its author's handle is
-- listed for that PR's repo (or for the wildcard repo '*'). Anyone not listed
-- is comment-only. Managed via `agent-code-review authors allow|deny|ls`.
CREATE TABLE IF NOT EXISTS allowed_authors (
  repo          TEXT NOT NULL,               -- 'owner/name' or '*' (all repos)
  github_handle TEXT NOT NULL,
  name          TEXT,
  email         TEXT,
  slack_id      TEXT,
  PRIMARY KEY (repo, github_handle)
);

-- Run-lock: a row per review cycle. An unfinished, recent row means a cycle is
-- (or may still be) in flight, so a new cycle skips. Advisory — DuckDB's
-- single-writer file lock is the hard backstop.
CREATE TABLE IF NOT EXISTS runs (
  id          TEXT      PRIMARY KEY,
  started_at  TIMESTAMP NOT NULL,
  finished_at TIMESTAMP,
  status      TEXT      NOT NULL,               -- running|done|failed
  host        TEXT,
  pid         INTEGER
);
