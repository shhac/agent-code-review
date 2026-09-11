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
  holds         JSON,                           -- named eligibility holds, {name: expiry}. The row is reviewable once every one is past; NULL/{} = eligible now. Manual adds/promotion clear them all.
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
  usage_raw          TEXT,
  -- What the agent was told, kept with the outcome it shaped. Steering lives on
  -- the QUEUE row and is retired with it, so without a copy here "why did it
  -- say that" becomes unanswerable the moment a review finishes.
  --
  -- NULL means NOT RECORDED, not "not steered": every row written before this
  -- column existed reads NULL whatever it was told, and those messages are
  -- unrecoverable. Nothing may render an absence here as an absence of
  -- steering.
  steering_message   TEXT,
  steering_by        TEXT,
  steering_at        TIMESTAMP,
  -- The agent returned APPROVED for a PR its author's policy forbids
  -- approving. The verdict column still says APPROVED, because that is what
  -- happened on GitHub; this says we asked for something else and did not get
  -- it. NULL/false on every row written before the check existed.
  policy_violation   BOOLEAN
);

-- Idempotent migrations for stores created before these columns existed.
-- Init applies this whole file on every boot; these are no-ops once applied.
-- (No NOT NULL here: DuckDB can't add constrained columns; DEFAULT 0
-- backfills the pre-existing rows, and Complete always writes a value.)
ALTER TABLE queue ADD COLUMN IF NOT EXISTS work_dir TEXT;
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

-- tailscale_login is the identity `tailscale serve` asserts in the
-- Tailscale-User-Login header, and it is what lets the dashboard answer "which
-- GitHub handle is this person". Deliberately its own column rather than
-- reusing `email`: they are the same string today, but email is free-text
-- contact detail that may be a shared alias or change independently, and this
-- one grants the right to steer somebody's review. Keeping them apart means a
-- collision in one cannot become an authorisation bug in the other.
--
-- One person can own several devices; Tailscale reports the USER on all of
-- them, so this needs no device dimension.
ALTER TABLE allowed_authors ADD COLUMN IF NOT EXISTS tailscale_login TEXT;
-- Case-insensitively unique: two rows sharing a login would let one person act
-- as another, which is the whole point of the column. DuckDB has no partial
-- indexes, but it treats NULLs as distinct, so the many rows without a login
-- coexist freely. An EMPTY STRING is not distinct and would collide on the
-- second row, which is why every write goes through nullText and stores unset
-- as NULL rather than ''.
CREATE UNIQUE INDEX IF NOT EXISTS allowed_authors_tailscale_login
  ON allowed_authors (lower(tailscale_login));

-- The `runs` table (one row per review CYCLE, the advisory run-lock) is
-- deliberately absent: reviews are dispatched individually as slots free, and
-- cross-process exclusion is the per-candidate CAS in Claim above. Stores
-- created before that change still carry the table and its rows; nothing
-- reads or writes it, and it is left alone rather than dropped so that
-- history survives.

ALTER TABLE history ADD COLUMN IF NOT EXISTS steering_message TEXT;
ALTER TABLE history ADD COLUMN IF NOT EXISTS steering_by TEXT;
ALTER TABLE history ADD COLUMN IF NOT EXISTS steering_at TIMESTAMP;
ALTER TABLE history ADD COLUMN IF NOT EXISTS policy_violation BOOLEAN;

-- eligible_at + hold_reason -> holds: one hold per row became one hold per
-- NAME per row, so that a discovery sweep can rewrite the two names it owns
-- without disturbing a hold set by anything else. The old pair could only
-- express the winner, so releasing one hold released whatever sat underneath.
--
-- Add, backfill, drop, in that order, every boot, exactly as engine_version
-- above: the ADDs keep the backfill valid on a store that never had the old
-- columns, and the DROPs retire them once their value has been carried across.
-- A hold is a debounce measured in minutes, so a store upgrading mid-hold
-- loses at most one deferral.
ALTER TABLE queue ADD COLUMN IF NOT EXISTS eligible_at TIMESTAMP;
ALTER TABLE queue ADD COLUMN IF NOT EXISTS hold_reason TEXT;
ALTER TABLE queue ADD COLUMN IF NOT EXISTS holds JSON;
UPDATE queue SET holds = json_object(hold_reason, strftime(eligible_at, '%Y-%m-%d %H:%M:%S'))
  WHERE holds IS NULL AND eligible_at IS NOT NULL AND hold_reason IS NOT NULL;
ALTER TABLE queue DROP COLUMN IF EXISTS eligible_at;
ALTER TABLE queue DROP COLUMN IF EXISTS hold_reason;
-- steering_editing_since briefly had its own column. It is one more named
-- instant on the row, and an instant that is always PAST defers nothing by
-- arithmetic, so it belongs in holds: same per-name merge, and it lands
-- atomically with the hold it dates instead of in a second statement.
ALTER TABLE queue DROP COLUMN IF EXISTS steering_editing_since;

-- Steering: a short instruction from the PR's author (or from the account
-- reviews are posted as) that shapes the NEXT review of that PR. Columns on
-- the queue row rather than a table of their own, because steering is exactly
-- a per-row attribute: same key, same lifetime, one per row. As a separate
-- table the 1:1 had to be maintained by hand at every site that retires a row,
-- and Complete needed an EXISTS subquery purely to re-derive "is the queue row
-- about to go". Here it goes when the row goes, structurally.
--
-- steering_by is the GitHub handle the dashboard proved via the roster, kept
-- so the prompt can attribute the instruction and an operator can see who
-- asked for what.
-- The retired standalone table. Unlike `runs` it is still declared, because
-- the backfill below reads it and must not depend on whether this store
-- predates the move. On a fresh store it is created empty and stays that way;
-- nothing reads or writes it after the backfill.
CREATE TABLE IF NOT EXISTS steering (
  repo       TEXT      NOT NULL,
  number     INTEGER   NOT NULL,
  message    TEXT      NOT NULL,
  set_by     TEXT      NOT NULL,
  set_at     TIMESTAMP NOT NULL,
  PRIMARY KEY (repo, number)
);

ALTER TABLE queue ADD COLUMN IF NOT EXISTS steering_message TEXT;
ALTER TABLE queue ADD COLUMN IF NOT EXISTS steering_by TEXT;
ALTER TABLE queue ADD COLUMN IF NOT EXISTS steering_at TIMESTAMP;

-- Carry across anything the standalone table already holds. Idempotent: the
-- UPDATE only writes rows whose column is still NULL, so re-running the schema
-- never resurrects steering a later edit cleared. The old table is left in
-- place rather than dropped, as with `runs`: nothing reads it, and dropping it
-- would discard history a live store may hold.
UPDATE queue SET
  steering_message = (SELECT s.message FROM steering s WHERE s.repo = queue.repo AND s.number = queue.number),
  steering_by      = (SELECT s.set_by  FROM steering s WHERE s.repo = queue.repo AND s.number = queue.number),
  steering_at      = (SELECT s.set_at  FROM steering s WHERE s.repo = queue.repo AND s.number = queue.number)
WHERE steering_message IS NULL
  AND EXISTS (SELECT 1 FROM steering s WHERE s.repo = queue.repo AND s.number = queue.number);

-- Author scoring: the diff facts a score is computed from, and the score
-- itself, frozen per review.
--
-- The diff columns are on BOTH tables on purpose. The queue row carries what
-- discovery saw (free: `gh pr list --json` already costs one GraphQL call
-- whatever fields it names), for display and for the dispatcher. The history
-- row carries what the review actually scored, which is a different number:
-- generated and vendored files have been taken out of it.
ALTER TABLE queue ADD COLUMN IF NOT EXISTS additions INTEGER;
ALTER TABLE queue ADD COLUMN IF NOT EXISTS deletions INTEGER;
ALTER TABLE queue ADD COLUMN IF NOT EXISTS changed_files INTEGER;

ALTER TABLE history ADD COLUMN IF NOT EXISTS additions INTEGER;
ALTER TABLE history ADD COLUMN IF NOT EXISTS deletions INTEGER;
ALTER TABLE history ADD COLUMN IF NOT EXISTS changed_files INTEGER;
-- The counts the score was actually computed from: raw totals minus the files
-- the repo's own .gitattributes marks linguist-generated or linguist-vendored
-- (plus any operator glob). Kept APART from the raw figures rather than
-- replacing them, so a history row can say "GitHub reports 12,000 lines, we
-- scored 43 of them, 2 files were excluded" instead of silently disagreeing
-- with the PR page.
ALTER TABLE history ADD COLUMN IF NOT EXISTS scored_additions INTEGER;
ALTER TABLE history ADD COLUMN IF NOT EXISTS scored_deletions INTEGER;
ALTER TABLE history ADD COLUMN IF NOT EXISTS excluded_files INTEGER;
-- The points, and the provenance of the number.
--
-- NULL here means NOT SCORED. That is a DELIBERATE departure from every other
-- numeric column on this table, which are NOT NULL DEFAULT 0 under the
-- convention that 0 means unknown (see cost_usd, fresh_tokens). Scoring has to
-- invert it because 0 is a LEGITIMATE score: a PR whose every line is
-- generated is worth exactly nothing, and that is a different fact from a PR
-- nobody has scored yet. Aggregates must treat NULL as absent, never as zero.
ALTER TABLE history ADD COLUMN IF NOT EXISTS score INTEGER;
-- 'derived' (computed from the rules) or 'manual' (an operator set it by
-- hand). A manual score is immune to recompute unless explicitly included:
-- a correction that a later retune silently undid would not be a correction.
ALTER TABLE history ADD COLUMN IF NOT EXISTS score_source TEXT;
-- The 16-hex-char hash of the ruleset that produced the score. This is what
-- makes "tuning changes scores going forwards" a fact rather than a promise:
-- retuning changes the hash, new reviews score under the new rules, existing
-- rows keep their points, and "which rows predate the current policy" is a
-- query that a targeted recompute can be aimed at.
ALTER TABLE history ADD COLUMN IF NOT EXISTS score_rules TEXT;
-- Why, for a manual correction.
ALTER TABLE history ADD COLUMN IF NOT EXISTS score_note TEXT;
ALTER TABLE history ADD COLUMN IF NOT EXISTS scored_at TIMESTAMP;
-- The attempt index the decay was applied at, frozen alongside the score.
--
-- Frozen rather than derived on read so that a row scored LATE (by
-- `score recompute --missing`, after a fetch failed at completion) lands on
-- the same index it would have had, and so a manual correction can pin one.
-- It counts REVISIONS, not verdicts: the number of distinct earlier head SHAs
-- of this PR that got a real verdict, plus one. A discussion re-review is a
-- second verdict at the SAME head, so replying to the bot must not cost the
-- author a decay step when no new code was written.
ALTER TABLE history ADD COLUMN IF NOT EXISTS score_attempt INTEGER;
-- The head the diff figures describe. A review's head can advance while it
-- runs (Complete's DELETE is gated on head_sha for exactly that reason), and
-- the file stats are fetched at claim time, so this records which revision
-- they belong to. A mismatch against head_sha means the diff moved under us
-- and the row is left unscored rather than credited with someone else's lines.
ALTER TABLE history ADD COLUMN IF NOT EXISTS diff_sha TEXT;
-- The size tier the score came from, frozen with it. Stored rather than
-- re-derived on read for the same reason the score is: the row may have been
-- scored under a ruleset whose tiers have since moved, and a bucket recomputed
-- under today's rules would explain a number today's rules did not produce.
ALTER TABLE history ADD COLUMN IF NOT EXISTS score_bucket TEXT;

-- The per-file detail a score was measured from: path, counts, and whether the
-- repo's own .gitattributes covered the file, as resolved at review time.
--
-- Metadata only, never patch text. It exists so that a change to OUR exclusion
-- policy (exclude_paths, use_gitattributes) is a recompute rather than a
-- re-fetch: the operator's globs are ours to re-run offline, and the repo's
-- verdict on each file is the one input that could not otherwise be recovered
-- without re-reading its declarations at the revision we reviewed. The same
-- escape-hatch reasoning as usage_raw: keeping the source means a later
-- question about it is a query rather than a migration and a data gap.
--
-- NULL means not recorded: a row from before this column, or one whose file
-- list GitHub truncated (a partial list must not be stored as though it were
-- complete). Such a row can only be repaired by `score refetch`.
ALTER TABLE history ADD COLUMN IF NOT EXISTS diff_files JSON;
