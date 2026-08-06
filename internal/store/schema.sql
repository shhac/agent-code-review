-- Persistent work queue + outcome history for agent-code-review.
-- Applied idempotently on every Store.Init.

-- The work queue: a row exists if and only if the PR has pending review work.
-- The primary key IS the "same PR queued once" guarantee, and completion
-- removes the row (atomically with its history insert); there is no status
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
  claim_host    TEXT,                           -- which daemon holds the claim (host + pid): lets a rebooted daemon clear its own dead claims immediately instead of waiting out the lease
  claim_pid     INTEGER,
  source        TEXT NOT NULL DEFAULT 'discovered', -- 'discovered' | 'manual'. Manual adds bypass the pre-review candidacy check (drafts and explicit re-review requests must go through).
  work_dir      TEXT,                           -- the engine's scratch workspace, set at claim time; its agent.log is the live review log
  eligible_at   TIMESTAMP,                      -- eligibility hold: the scheduler skips this row until then. NULL = eligible now. Manual adds/promotion clear it.
  hold_reason   TEXT,                           -- why the hold exists: 'cooldown' (recently reviewed by us) | 'settling' (PR updated too recently)
  PRIMARY KEY (repo, number)
);

-- Append-only outcome history: one row per completed queue item, including
-- SKIPPED and ERROR outcomes. Duplicates per (repo, number) are expected;
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
  model         TEXT,                           -- managed model; NULL when the engine/default selected it
  effort        TEXT,                           -- managed reasoning effort; NULL when the model/default selected it
  engine_version TEXT,                          -- version of the CLI that ran this review; NULL when unavailable
  reviewed_at   TIMESTAMP NOT NULL,
  duration_secs INTEGER   NOT NULL DEFAULT 0,   -- claim-to-completion elapsed; 0 for rows predating the column and for manual skips
  work_dir      TEXT,                           -- the engine workspace used, kept for postmortem log access
  tokens_used   INTEGER   NOT NULL DEFAULT 0,   -- engine-reported token spend; 0 when unknown
  cost_usd      DOUBLE                          -- engine-reported API-rate valuation of the run, NOT money charged
                          NOT NULL DEFAULT 0,   -- (on a subscription, what the tokens would cost at API rates); 0 when unreported
  est_cost_usd  DOUBLE    NOT NULL DEFAULT 0,   -- our own valuation: token classes priced at the model's rates, frozen at
                                                -- completion. 0 means no estimate was possible, never that the run was free.
  -- tokens_used split into the only two kinds anything downstream asks about:
  -- work the model actually processed, and context it re-read from cache.
  -- Cached re-reads dominate a long agentic session, so a total including them
  -- runs ~28x one that excludes them; fresh_tokens is therefore the only token
  -- figure comparable between engines that report differently. Each driver
  -- decides the split, so a reader never has to know which engine ran.
  -- fresh_tokens 0 means unknown, not zero work: rows recorded before this
  -- column by an engine whose total was cache-inflated cannot be recovered.
  fresh_tokens       INTEGER NOT NULL DEFAULT 0,
  input_tokens       INTEGER NOT NULL DEFAULT 0,
  output_tokens      INTEGER NOT NULL DEFAULT 0,
  cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens   INTEGER NOT NULL DEFAULT 0,  -- part of output_tokens, not an addition to it
  -- What the engine actually said about usage, verbatim, one JSON entry per
  -- invocation. Everything above is a projection of this; keeping the source
  -- means a pricing question about a field we never modelled (claude's 5m/1h
  -- cache tiers, its server tool calls) is a query, not a migration.
  usage_raw          TEXT
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
ALTER TABLE history ADD COLUMN IF NOT EXISTS model TEXT;
ALTER TABLE history ADD COLUMN IF NOT EXISTS effort TEXT;
-- codex_version -> engine_version: the column outlived its single-engine name
-- once a second driver (claude) could produce rows.
--
-- Add, backfill, drop, in that order, every boot. The add and the drop look
-- redundant together, but each covers a path the other doesn't: the ADD keeps
-- the backfill's UPDATE valid on a store that never had the column (a fresh
-- one, or one already migrated by an earlier boot), and the DROP retires it
-- once its value has been copied across. Doing both means a store upgrading
-- from ANY earlier version -- including one that skipped the release where
-- engine_version landed -- keeps its recorded versions.
ALTER TABLE history ADD COLUMN IF NOT EXISTS codex_version TEXT;
ALTER TABLE history ADD COLUMN IF NOT EXISTS engine_version TEXT;
UPDATE history SET engine_version = codex_version WHERE engine_version IS NULL;
ALTER TABLE history DROP COLUMN IF EXISTS codex_version;
-- Per-review spend. Rows predating the column, and every codex row (codex
-- prints a token trailer but no cost), read as 0.
ALTER TABLE history ADD COLUMN IF NOT EXISTS cost_usd DOUBLE DEFAULT 0;
-- Token split, in the same add-backfill-drop shape as engine_version above.
-- A short-lived earlier form recorded claude's raw four-way usage; the ADDs
-- keep the recovery UPDATE valid on a store that never had those columns, and
-- the DROPs retire them once folded into fresh_tokens.
ALTER TABLE history ADD COLUMN IF NOT EXISTS fresh_tokens INTEGER DEFAULT 0;
ALTER TABLE history ADD COLUMN IF NOT EXISTS cache_read_tokens INTEGER DEFAULT 0;
ALTER TABLE history ADD COLUMN IF NOT EXISTS input_tokens INTEGER DEFAULT 0;
ALTER TABLE history ADD COLUMN IF NOT EXISTS output_tokens INTEGER DEFAULT 0;
ALTER TABLE history ADD COLUMN IF NOT EXISTS cache_creation_tokens INTEGER DEFAULT 0;
UPDATE history SET fresh_tokens = input_tokens + output_tokens + cache_creation_tokens
  WHERE fresh_tokens = 0 AND input_tokens + output_tokens + cache_creation_tokens > 0;
-- The per-class columns. input_tokens and output_tokens above were briefly
-- retired into fresh_tokens when nothing read them apart; pricing reads them
-- apart, so they stay. cache_write_tokens is a new name rather than a revived
-- cache_creation_tokens, whose rows were already folded away by the UPDATE
-- above and must not be read back with the new meaning.
ALTER TABLE history ADD COLUMN IF NOT EXISTS cache_write_tokens INTEGER DEFAULT 0;
ALTER TABLE history ADD COLUMN IF NOT EXISTS reasoning_tokens INTEGER DEFAULT 0;
ALTER TABLE history ADD COLUMN IF NOT EXISTS usage_raw TEXT;
-- Our valuation, for the engine that reports none. Rows predating it read 0
-- (no estimate), and the scheduler backfills any row that has a class split
-- but no estimate yet.
ALTER TABLE history ADD COLUMN IF NOT EXISTS est_cost_usd DOUBLE DEFAULT 0;
-- Rows predating the split at all. Their tokens_used is trustworthy as a
-- fresh count only from an engine that never counted cached re-reads, which
-- means every engine except claude. Naming claude here is safe by
-- construction rather than a guess about the future: any engine wired after
-- this column existed writes fresh_tokens itself, so a 0 on its rows is a
-- real zero and re-running this UPDATE leaves it at 0 either way.
UPDATE history SET fresh_tokens = tokens_used
  WHERE fresh_tokens = 0 AND cache_read_tokens = 0 AND engine IS DISTINCT FROM 'claude';
ALTER TABLE history DROP COLUMN IF EXISTS cache_creation_tokens;

-- Per-repo author roster: which GROUP an author belongs to for a repo. The
-- group names a policy defined in config (what we may do with their PRs, which
-- engine reviews them, what extra instruction the agent gets); only membership
-- lives here, because membership is what churns and varies per repo. A row for
-- the PR's repo wins over a row for the wildcard repo '*'; an author with no
-- row resolves through config's unlisted fallback. Managed via
-- `agent-code-review authors set|rm|ls`.
--
-- The table keeps its original name. Renaming it would gain nothing a comment
-- cannot, and would cost every existing store a data move.
CREATE TABLE IF NOT EXISTS allowed_authors (
  repo          TEXT NOT NULL,               -- 'owner/name' or '*' (all repos)
  github_handle TEXT NOT NULL,
  group_name    TEXT,                        -- config group name; NULL reads as the built-in 'approver'
  name          TEXT,
  email         TEXT,
  slack_id      TEXT,
  PRIMARY KEY (repo, github_handle)
);

-- Groups arrived after the table did. Every pre-existing row WAS the allow
-- list, and the allow list meant exactly one thing: this author may be
-- approved. That is the built-in 'approver' group, so the backfill is a
-- rename of an implicit policy rather than a new decision, and an upgraded
-- store behaves identically with no user action.
ALTER TABLE allowed_authors ADD COLUMN IF NOT EXISTS group_name TEXT;
UPDATE allowed_authors SET group_name = 'approver' WHERE group_name IS NULL;

-- Run-lock: a row per review cycle. An unfinished, recent row means a cycle is
-- (or may still be) in flight, so a new cycle skips. Advisory: DuckDB's
-- single-writer file lock is the hard backstop.
CREATE TABLE IF NOT EXISTS runs (
  id          TEXT      PRIMARY KEY,
  started_at  TIMESTAMP NOT NULL,
  finished_at TIMESTAMP,
  status      TEXT      NOT NULL,               -- running|done|failed
  host        TEXT,
  pid         INTEGER
);
