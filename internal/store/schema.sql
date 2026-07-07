-- Persistent queue + review history for agent-code-review.
-- Applied idempotently on every Store.Init.

CREATE TABLE IF NOT EXISTS candidates (
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
  status        TEXT    NOT NULL DEFAULT 'queued', -- queued|reviewing|reviewed|skipped|error
  discovered_at TIMESTAMP,
  PRIMARY KEY (repo, number)
);

-- One row per review the engine has completed. The most recent row per
-- (repo, number) drives Refreshed detection: a candidate is REFRESHED when its
-- current head_sha differs from the head_sha we last reviewed.
CREATE TABLE IF NOT EXISTS reviews (
  repo        TEXT      NOT NULL,
  number      INTEGER   NOT NULL,
  head_sha    TEXT      NOT NULL,
  verdict     TEXT      NOT NULL,               -- APPROVE|COMMENT|ERROR
  engine      TEXT,
  reviewed_at TIMESTAMP NOT NULL
);

-- Per-repo approver allow-list. A PR may receive an APPROVE only when its
-- author's handle is listed for that PR's repo (or for the wildcard repo '*').
-- Anyone not listed is comment-only. This is the source of truth for approval,
-- managed via `agent-code-review approvers`.
CREATE TABLE IF NOT EXISTS approvers (
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
