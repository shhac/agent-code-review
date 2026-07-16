package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

const usageText = `agent-code-review: PR review queue + scheduler for AI agents

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
  queue promote <owner/repo> <number>                Review now: top of queue, clears holds, treated as manual
  queue skip <owner/repo> <number>                   Record a SKIPPED outcome (re-eligible on new commits)
  queue log <owner/repo> <number> [-f]               Stream the review agent's log (live or postmortem)

  repos ls | add <owner/repo> [--allowed-authors-only] | rm <owner/repo>
                                                     Manage the watched repos (config)

  authors ls [--repo R]                              List allowed authors
  authors allow <owner/repo|*> <handle> [--name ...] Allow an author's PRs to be approved
  authors deny <owner/repo|*> <handle>               Make an author's PRs comment-only again

  prompts show | set <slot> <text> | unset | preview Manage the review prompts
                                                     (slots: main, on-approve, on-comment, on-reject)
  prompts rules ls | add --name N --prompt T [--outcome ...] | rm <name>
                                                     Conditional prompt fragments, optionally
                                                     routed under a post-outcome bullet

  config init | path | show                         Starter config / file location / full dump
  config list | get <key> | set <key> <v> | unset   Typed settings (schedule, candidates, codex, ...)
  usage                                              This help

CONFIG: ~/.config/agent-code-review/config.json (respects XDG_CONFIG_HOME).
  Most settings reload live (within ~30s): cadence, parallelism, usage floors,
  repos, prompts, codex settings. Restart only for the loop on/off switches,
  dashboard address, and Tailscale mode.
  Everything tunable lives here: watched repos, the approval allow-list, age
  thresholds (14d New / 21d Refreshed), schedule cadence + parallelism, the
  review engine + main prompt + rules, the DuckDB path, and dashboard/Tailscale.
  See config.example.json. No repos or GitHub handles are hardcoded.

CANDIDATES (discovery is deterministic: gh + rules, never the LLM):
  NEW:       open, not draft, review requested, never reviewed, not currently
              approved, ≤ new_max_age_days
  REFRESHED: open, not draft, re-review requested, not currently approved, head
              SHA differs from our last recorded review, ≤ refreshed_max_age_days
  Repos in allowed_authors_only_repos additionally require the PR author to be
  on the allowed-authors list. Manual adds (queue add / dashboard) fetch live
  metadata via gh and reject closed/merged PRs.
  Discovered candidates can carry an eligibility hold: settling (PR updated
  within candidates.quiet_period) or cooldown (we reviewed it within
  candidates.rereview_cooldown). Held rows sit visibly in the queue but are
  skipped by review cycles until eligible_at; queue promote or a manual add
  bypasses holds.

APPROVAL: allowed authors (whose PRs WE may approve; we are the reviewer) are
  stored in DuckDB, per repo (manage with 'authors'). The assembled prompt always
  carries a built-in approval directive that DEFAULTS to comment-only; an APPROVE
  is permitted only when the author is allowed for this repo and it isn't a
  self-authored PR. Only this PR's author↔allowed pair is passed to the engine,
  never the whole list.

REVIEW: the engine (codex) receives the main prompt + approval directive +
  post-outcome instructions (review.on_approve / on_comment / on_reject) + any
  matching rule fragments, performs the review itself, posts to GitHub, and
  reports back what it did (APPROVED|COMMENTED|REQUESTED_CHANGES|SKIPPED).
  The tool assumes ONLY the gh and codex CLIs. Anything else (skills, extra
  CLIs, team conventions) belongs in YOUR prompts, never in shipped defaults.

STORE: DuckDB via the duckdb CLI (subprocess, CGO-free). Requires the duckdb
  binary on PATH (brew install duckdb); override with AGENT_CODE_REVIEW_DUCKDB_PATH.

OUTPUT: NDJSON to stdout; errors {error, fixable_by, hint} to stderr, exit 1.

DETAIL: Run "<command> usage" for per-command docs and examples
  (queue usage, repos usage, authors usage, prompts usage, prompts rules usage,
  config usage).`

const queueUsageText = `queue: The review queue (stored in DuckDB)

COMMANDS:
  queue ls [--repo owner/name]
    List pending candidates in review order: explicit queue positions first,
    then FIFO by first discovery (New before Refreshed, then lowest PR number
    as same-sweep tiebreaks). One NDJSON record per candidate. A row with
    claimed_at set is being reviewed right now; a row with eligible_at in the
    future is on hold (hold_reason: cooldown = we reviewed it recently,
    settling = the PR was pushed/edited too recently) and is skipped by
    review cycles until then.

  queue add <owner/repo> <number>
    Add a PR by hand: live metadata (title/author/SHA) is fetched via gh, and
    closed/merged PRs are rejected. An already-queued PR just refreshes its
    metadata. Manual adds are ALWAYS reviewed: they bypass the pre-review
    candidacy recheck that discovered candidates get (which skips PRs that
    were approved/closed/merged while waiting in the queue), so explicit
    re-review requests and draft reviews go through.

  queue promote <owner/repo> <number>
    "Review this now": float the PR to the very top, clear any eligibility
    hold (cooldown/settling), and escalate it to a manual add (bypassing the
    pre-review candidacy recheck). Reordering in the dashboard does NONE of
    that; a drag changes only the position and respects holds.

  queue skip <owner/repo> <number>
    Record a SKIPPED outcome and drop the PR from the queue. It becomes
    eligible again when new commits arrive, or re-add with queue add.

  queue rm <owner/repo> <number>
    Remove a PR from the queue entirely, recording nothing.

  queue log <owner/repo> <number> [-f|--follow]
    Stream the review agent's output. Live while the review runs (the engine
    tees into <workdir>/agent.log); the log of the most recent finished
    review otherwise. --follow keeps tailing until interrupted.

EXAMPLES:
  agent-code-review queue ls --repo example-org/example-repo
  agent-code-review queue add example-org/example-repo 123
  agent-code-review queue promote example-org/example-repo 123

NOTES: the queue holds only pending work. Completed reviews (including
skips and errors) live in the outcome history shown by the dashboard's
Recent reviews. The dashboard offers the same add/reorder operations.`

const reposUsageText = `repos: The watched repos (stored in config.json)

Discovery, the dashboard add-PR form, and the scheduler only operate on repos
in this list. Ships empty: nothing is watched until you add it.

COMMANDS:
  repos ls                 One record per watched repo, with its author scope
  repos add <owner/repo> [--allowed-authors-only]
    Add a repo (idempotent; re-running toggles the scope). By default any open
    PR is discovered; --allowed-authors-only scopes discovery to PRs authored
    by allowed authors, for repos where reviewing every PR would be noise.
  repos rm <owner/repo>    Stop watching a repo (clears its scope too)

EXAMPLES:
  agent-code-review repos add example-org/example-repo
  agent-code-review repos ls`

const authorsUsageText = `authors: Allowed authors, whose PRs WE may approve (stored in DuckDB)

We are the reviewer. This list controls whose PRs this tool will APPROVE; it
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

const promptsUsageText = `prompts: The review prompts (stored in config.json)

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
  prompts rules ...            Conditional prompt fragments (prompts rules usage)
  prompts preview [--author-not-allowed] [--candidate-type new|refreshed]
                  [--repo owner/name] [--author-is-gh-user] [--explain]
    Print the fully assembled prompt for a synthetic candidate you shape with
    flags, so any rule can be made to fire; --explain adds a per-rule trace of
    what matched and why.

EXAMPLES:
  agent-code-review prompts set main "Review this PR thoroughly via the gh CLI and leave one review."
  agent-code-review prompts set on-approve "Notify the team channel per our conventions."
  agent-code-review prompts preview --author-not-allowed --explain

NOTES: put workspace-specific conventions (channels, emoji, extra CLIs) in
these slots; the tool itself assumes only the gh and codex CLIs. Conditional
extras (per repo / candidate type / allow-list branch), including ones that
attach to a specific outcome, are rules; manage them with 'prompts rules'
(prompts rules usage).`

const rulesUsageText = `prompts rules: Conditional prompt fragments (stored in config.json)

A rule adds EXTRA instructions to the assembled prompt when its condition
matches the candidate. Comment-only vs approval is a separate built-in
directive; you never need a rule for it.

Without an --outcome the fragment appends to the prompt body (fires for any
outcome). WITH an --outcome it attaches under that post-outcome bullet
(approve / comment / reject) alongside the base on_* slot, so behaviour can
branch deterministically, e.g. on the allowed-authors list, without relying on
prompt phrasing. Fragments are ADDITIVE: the base slot is the shared part, the
rule carries the conditional part.

CONDITIONS (unset = wildcard; all set must hold):
  --author-allowed        PR author IS on the allowed-authors list for the repo
  --author-not-allowed     PR author is NOT on it (mutually exclusive with above)
  --author-is-gh-user      self-authored (author == our gh user)
  --author-not-gh-user     NOT self-authored (mutually exclusive with above)
  --candidate-type new|refreshed
  --repo owner/name        repeatable, any-of
Note: --author-allowed means "on the allow-list," not "was approvable"; a
self-authored PR by an allow-listed author is still comment-only, yet counts
as author-allowed. To split self-review out, add --author-not-gh-user to the
allow-list rules and give self-authored PRs their own --author-is-gh-user rule.

COMMANDS:
  prompts rules ls         One record per rule, in config order (NDJSON)
  prompts rules add --name N --prompt TEXT [--outcome approve|comment|reject] [conditions]
    Append a rule. Name must be unique; a rule with both allow-list flags is
    rejected (it could never match).
  prompts rules rm <name>  Remove rule(s) with this name (case-insensitive)

EXAMPLES:
  # On comment, branch the Slack reaction on the allow-list:
  agent-code-review prompts rules add --name comment-not-allowed --outcome comment \
    --author-not-allowed --prompt "React :verified: :lizard: on the PR's Slack message."
  agent-code-review prompts rules add --name comment-allowed --outcome comment \
    --author-allowed --prompt "React :git-re-request: :bad-lizard: on the PR's Slack message."
  agent-code-review prompts rules ls

NOTES: put the shared part (e.g. locating the Slack message) in the on-comment
slot via 'prompts set', and the branch-specific part in these rules. See exactly
how they assemble with 'prompts preview [--author-not-allowed] [--explain]'.`

const configUsageText = `config: Persisted settings (stored in config.json)

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
  schedule.enabled                     true|false: daemon runs review cycles
  schedule.interval                    review cadence, e.g. 1m (idle cycles are no-ops)
  discovery.enabled                    true|false: daemon scrapes for candidates
  discovery.interval                   scrape cadence, e.g. 10m (gh only, no LLM)
  schedule.max_parallel                1..32 concurrent reviews per cycle
  schedule.usage_floor.5h_percent      pause reviews when the 5h Codex window has
                                       less than this % remaining (default 10, 0 off)
  schedule.usage_floor.weekly_percent  same for the weekly window (default 10, 0 off)
  candidates.new_max_age_days          New candidate window (default 14)
  candidates.refreshed_max_age_days    Refreshed candidate window (default 21)
  candidates.rereview_cooldown         hold after our own review before re-discovery
                                       (default 90m, 0s disables)
  candidates.quiet_period              PR must go untouched this long before discovery
                                       accepts it (default 15m, 0s disables)
  review.engine                        codex
  codex.bin | codex.model | codex.effort | codex.sandbox
  dashboard.addr                       listen address (default :8330)
  dashboard.tailscale.mode             "" | serve | funnel
  dashboard.usage_poll_interval        Codex usage refresh cadence (default 10m)
  store.path                           DuckDB file location

EXAMPLES:
  agent-code-review config set schedule.interval 15m
  agent-code-review config get schedule.interval
  agent-code-review config list

NOTES: repos, authors, and prompts have their own command groups (repos usage,
authors usage, prompts usage); rules live under prompts (prompts rules usage).
codex.args is edited in the file directly (config path).`

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
