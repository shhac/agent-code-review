package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

const usageText = `agent-code-review — PR review queue + scheduler for AI agents

WHAT IT DOES:
  Discovers candidate PRs across configured repos (via gh), keeps a DuckDB-backed
  queue, and reviews them by handing an assembled prompt to a pluggable engine
  (default: codex). Ships a dashboard you can expose over Tailscale.

COMMANDS:
  serve [--http :8330] [--tailscale serve|funnel]   Run the daemon (dashboard + loops)
        [--no-discovery] [--no-reviews] [--no-schedule=both]
                                                     Per-loop overrides for this boot; config
                                                     (discovery.enabled/schedule.enabled) sets defaults
  run  [--once]                                      Run a single review cycle, then exit

  queue ls [--repo R]                                List pending candidates (NDJSON)
  queue add <owner/repo> <number>                    Add a PR to the queue
  queue rm <owner/repo> <number>                     Remove a PR
  queue promote <owner/repo> <number>                Float a PR to the top
  queue skip <owner/repo> <number>                   Record a SKIPPED outcome (re-eligible on new commits)

  repos ls | add <owner/repo> [--allowed-authors-only] | rm <owner/repo>
                                                     Manage the watched repos (config)

  authors ls [--repo R]                              List allowed authors
  authors allow <owner/repo|*> <handle> [--name ...] Allow an author's PRs to be approved
  authors deny <owner/repo|*> <handle>               Make an author's PRs comment-only again

  prompts show | set <slot> <text> | unset | preview Manage the review prompts
                                                     (slots: main, on-approve, on-comment, on-reject)

  config init | path | show                         Starter config / file location / full dump
  config list | get <key> | set <key> <v> | unset   Typed settings (schedule, candidates, codex, ...)
  usage                                              This help

CONFIG: ~/.config/agent-code-review/config.json (respects XDG_CONFIG_HOME).
  Everything tunable lives here — watched repos, the approval allow-list, age
  thresholds (14d New / 21d Refreshed), schedule cadence + parallelism, the
  review engine + main prompt + rules, the DuckDB path, and dashboard/Tailscale.
  See config.example.json. No repos or GitHub handles are hardcoded.

CANDIDATES (discovery is deterministic — gh + rules, never the LLM):
  NEW       — open, not draft, review requested, never reviewed, not currently
              approved, ≤ new_max_age_days
  REFRESHED — open, not draft, re-review requested, not currently approved, head
              SHA differs from our last recorded review, ≤ refreshed_max_age_days
  Repos in allowed_authors_only_repos additionally require the PR author to be
  on the allowed-authors list. Manual adds (queue add / dashboard) fetch live
  metadata via gh and reject closed/merged PRs.

APPROVAL: allowed authors (whose PRs WE may approve — we are the reviewer) are
  stored in DuckDB, per repo (manage with 'authors'). The assembled prompt always
  carries a built-in approval directive that DEFAULTS to comment-only; an APPROVE
  is permitted only when the author is allowed for this repo and it isn't a
  self-authored PR. Only this PR's author↔allowed pair is passed to the engine —
  never the whole list.

REVIEW: the engine (codex) receives the main prompt + approval directive +
  post-outcome instructions (review.on_approve / on_comment / on_reject) + any
  matching rule fragments, performs the review itself, posts to GitHub, and
  reports back what it did (APPROVED|COMMENTED|REQUESTED_CHANGES|SKIPPED).
  The tool assumes ONLY the gh and codex CLIs. Anything else — skills, extra
  CLIs, team conventions — belongs in YOUR prompts, never in shipped defaults.

STORE: DuckDB via the duckdb CLI (subprocess, CGO-free). Requires the duckdb
  binary on PATH (brew install duckdb); override with AGENT_CODE_REVIEW_DUCKDB_PATH.

OUTPUT: NDJSON to stdout; errors {error, fixable_by, hint} to stderr, exit 1.

DETAIL: Run "<command> usage" for per-command docs and examples
  (queue usage, repos usage, authors usage, prompts usage, config usage).`

const queueUsageText = `queue — The review queue (stored in DuckDB)

COMMANDS:
  queue ls [--repo owner/name]
    List pending candidates in review order: explicit queue positions first,
    then New before Refreshed, then lowest PR number. One NDJSON record per
    candidate. A row with claimed_at set is being reviewed right now.

  queue add <owner/repo> <number>
    Add a PR by hand: live metadata (title/author/SHA) is fetched via gh, and
    closed/merged PRs are rejected. An already-queued PR just refreshes its
    metadata. (The scheduler also adds candidates via discovery.)

  queue promote <owner/repo> <number>
    Float a PR to the very top of the queue (across types).

  queue skip <owner/repo> <number>
    Record a SKIPPED outcome and drop the PR from the queue. It becomes
    eligible again when new commits arrive, or re-add with queue add.

  queue rm <owner/repo> <number>
    Remove a PR from the queue entirely, recording nothing.

EXAMPLES:
  agent-code-review queue ls --repo example-org/example-repo
  agent-code-review queue add example-org/example-repo 123
  agent-code-review queue promote example-org/example-repo 123

NOTES: the queue holds only pending work — completed reviews (including
skips and errors) live in the outcome history shown by the dashboard's
Recent reviews. The dashboard offers the same add/reorder operations.`

const reposUsageText = `repos — The watched repos (stored in config.json)

Discovery, the dashboard add-PR form, and the scheduler only operate on repos
in this list. Ships empty: nothing is watched until you add it.

COMMANDS:
  repos ls                 One record per watched repo, with its author scope
  repos add <owner/repo> [--allowed-authors-only]
    Add a repo (idempotent; re-running toggles the scope). By default any open
    PR is discovered; --allowed-authors-only scopes discovery to PRs authored
    by allowed authors — for repos where reviewing every PR would be noise.
  repos rm <owner/repo>    Stop watching a repo (clears its scope too)

EXAMPLES:
  agent-code-review repos add example-org/example-repo
  agent-code-review repos ls`

const authorsUsageText = `authors — Allowed authors: whose PRs WE may approve (stored in DuckDB)

We are the reviewer. This list controls whose PRs this tool will APPROVE — it
is not about who can approve. Authors not on the list still get reviewed, but
comment-only. Only the single author<->allowed pair for the PR under review is
ever passed to the engine, never the whole list.

COMMANDS:
  authors ls [--repo <owner/repo|*>]
    List allowed authors, optionally for one repo. "*" entries apply everywhere.

  authors allow <owner/repo|*> <github-handle> [--name N] [--email E] [--slack-id S]
    Allow an author's PRs to be approved for a repo (upserts metadata).

  authors deny <owner/repo|*> <github-handle>
    Remove an author (their PRs become comment-only again).

EXAMPLES:
  agent-code-review authors allow example-org/example-repo example-handle --name "Example Engineer"
  agent-code-review authors allow '*' example-handle     # approvable on every repo
  agent-code-review authors deny '*' example-handle

NOTES: matching is case-insensitive. Self-authored PRs are always comment-only
regardless of this list.`

const promptsUsageText = `prompts — The review prompts (stored in config.json)

The assembled prompt = main prompt + candidate context + built-in approval
directive + post-outcome instructions + matching rules. The engine driver
appends a reporting instruction (final message = JSON verdict) on top.

SLOTS:
  main         The core review instructions
  on-approve   What to do after submitting an approving review
  on-comment   What to do after commenting without approving
  on-reject    What to do after requesting changes

COMMANDS:
  prompts show                 One record per slot (notes main_prompt_path override)
  prompts set <slot> <text>    Set a slot (multi-word text can be one quoted arg)
  prompts unset <slot>         Clear a slot
  prompts preview [--author-not-allowed]
    Print the fully assembled prompt for a synthetic candidate — exactly what
    the agent receives (allowed-author variant by default).

EXAMPLES:
  agent-code-review prompts set main "Review this PR thoroughly via the gh CLI and leave one review."
  agent-code-review prompts set on-approve "Notify the team channel per our conventions."
  agent-code-review prompts preview --author-not-allowed

NOTES: put workspace-specific conventions (channels, emoji, extra CLIs) in
these slots — the tool itself assumes only the gh and codex CLIs. Conditional
extras (per repo / per candidate type) live in review.rules in config.json.`

const configUsageText = `config — Persisted settings (stored in config.json)

COMMANDS:
  config init            Write the annotated starter config (refuses to overwrite)
  config path            Print the config file location
  config show            Print the whole resolved config
  config list            All keys with values and descriptions
  config get <key>       Show one value
  config set <key> <v>   Set one value (validated)
  config unset <key>     Reset a key to its default

KEYS:
  gh_user                              self-review detection (empty = derive via gh)
  schedule.enabled                     true|false — daemon runs review cycles
  schedule.interval                    review cadence, e.g. 30m
  discovery.enabled                    true|false — daemon scrapes for candidates
  discovery.interval                   scrape cadence, e.g. 10m (gh only, no LLM)
  schedule.max_parallel                1..32 concurrent reviews per cycle
  candidates.new_max_age_days          New candidate window (default 14)
  candidates.refreshed_max_age_days    Refreshed candidate window (default 21)
  review.engine                        codex
  codex.bin | codex.model | codex.sandbox
  dashboard.addr                       listen address (default :8330)
  dashboard.tailscale.mode             "" | serve | funnel
  dashboard.usage_poll_interval        Codex usage refresh cadence (default 10m)
  store.path                           DuckDB file location

EXAMPLES:
  agent-code-review config set schedule.interval 15m
  agent-code-review config get schedule.interval
  agent-code-review config list

NOTES: repos, authors, and prompts have their own command groups (repos usage,
authors usage, prompts usage). review.rules and codex.args are edited in the
file directly (config path).`

func registerUsage(root *cobra.Command) {
	root.AddCommand(&cobra.Command{
		Use:   "usage",
		Short: "Print concise documentation (LLM-optimized)",
		Args:  cobra.NoArgs,
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Println(strings.TrimSpace(usageText))
		},
	})
}

// registerGroupUsage attaches the family's conventional per-group `usage`
// subcommand: a reference card with syntax, behavior, and examples.
func registerGroupUsage(parent *cobra.Command, verb, text string) {
	parent.AddCommand(&cobra.Command{
		Use:   "usage",
		Short: "Print " + verb + " command documentation (LLM-optimized)",
		Args:  cobra.NoArgs,
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Println(strings.TrimSpace(text))
		},
	})
}
