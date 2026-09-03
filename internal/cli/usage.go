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
  (codex or claude; default: codex). Ships a dashboard you can expose over Tailscale.

COMMANDS:
  serve [--http :8330] [--tailscale serve|funnel]   Run the daemon (dashboard + loops)
        [--no-discovery] [--no-reviews] [--no-schedule=both]
                                                     Per-loop overrides for this boot; config
                                                     (discovery.enabled/schedule.enabled) sets defaults
  run  [--once]                                      Run a single review cycle, then exit
                                                     (stdout: outcome records + a summary;
                                                     stderr: cycle progress logs)

  queue ls [--repo R]                                List pending candidates (NDJSON)
  queue add <owner/repo> <number>                    Add a PR to the queue
  queue rm <owner/repo> <number>                     Remove a PR
  queue promote <owner/repo> <number>                Review now: top of queue, clears holds, treated as manual
  queue skip <owner/repo> <number>                   Record a SKIPPED outcome (re-eligible on new commits)
  queue log <owner/repo> <number> [-f]               Stream the review agent's log (live or postmortem)

  repos ls | add <owner/repo> [--unlisted G] | rm <owner/repo>
                                                     Manage the watched repos (config)

  authors ls [--repo R] [--group G]                  Roster entries + the policy each resolves to
  authors set <owner/repo|*> <handle> <group>        Put an author in a group for a repo
  authors rm <owner/repo|*> <handle>                 Remove a roster entry (falls back to authors.unlisted)
  authors groups                                     The defined groups and what each one grants
  authors who <handle> --repo <owner/repo>           Resolve one author's policy, with the deciding layer

  prompts show | set <slot> <text> | unset | preview Manage the review prompts
                                                     (slots: main, on-approve, on-comment, on-reject, resume)
  prompts rules ls | add --name N --prompt T [--outcome ...] | rm <name>
                                                     Conditional prompt fragments, optionally
                                                     routed under a post-outcome section

  config init | path | show                         Starter config / file location / full dump
  config list | get <key> | set <key> <v> | unset   Typed settings (schedule, candidates, codex, ...)
  doctor                                            Check this machine can run a review
  usage                                              This help

CONFIG: ~/.config/agent-code-review/config.json (respects XDG_CONFIG_HOME).
  Most settings reload live (within ~30s): cadence, parallelism, usage floors,
  repos, prompts, codex settings. Restart only for the loop on/off switches,
  dashboard address, and Tailscale mode.
  Everything tunable lives here: watched repos, the author groups, age
  thresholds (14d New / 21d Refreshed / 14d Discussion), schedule cadence + parallelism, the
  review engine + main prompt + rules, the DuckDB path, and dashboard/Tailscale.
  See config.example.json. No repos or GitHub handles are hardcoded.

CANDIDATES (discovery is deterministic: gh + rules, never the LLM):
  NEW:       open, not draft, review requested, never reviewed, not currently
              approved, ≤ new_max_age_days
  REFRESHED: open, not draft, re-review requested, not currently approved, head
              SHA differs from our last recorded review, ≤ refreshed_max_age_days
  DISCUSSION: same head SHA we already reviewed, but a person (not a bot, not
              us) has commented, replied inline, or resolved a thread since that
              review, ≤ discussion_max_age_days. The code is unchanged, so the
              review engine re-judges its standing findings against what was
              said rather than deriving the PR again.
  An author whose resolved group has review level "ignore" is never discovered
  at all; a manual add still reviews them. Manual adds (queue add / dashboard)
  fetch live metadata via gh and reject closed/merged PRs.
  Discovered candidates can carry an eligibility hold: settling (PR updated
  within candidates.quiet_period) or cooldown (we reviewed it within
  candidates.rereview_cooldown). Held rows sit visibly in the queue but are
  skipped by review cycles until eligible_at; queue promote or a manual add
  bypasses holds.

AUTHOR GROUPS: an author belongs to ONE group per repo, and the group IS the
  policy. Review level is an ordered ladder:
    ignore   never discovered (a manual queue add still reviews)
    comment  reviewed, never approved
    approve  approvable when the review warrants it
  A group also carries the engine, model, effort, and an extra prompt fragment;
  authors.overrides narrows any of that per handle, optionally per repo. Empty
  fields inherit; prompts accumulate.
  WHO is in a group lives in DuckDB, per repo ('authors set'); the groups
  themselves live in config, beside the prompts they carry. Resolution: the
  repo's roster row, else the '*' row, else authors.unlisted[repo], else
  authors.unlisted['*']. 'authors who' shows which layer decided what.
  The assembled prompt always carries a built-in approval directive that
  DEFAULTS to comment-only. An APPROVE needs an approve-level group AND a PR
  that isn't self-authored: the self-review veto sits above the cascade and no
  group or override can lift it. Only this PR's own resolved policy reaches the
  engine, never the roster.
  A group naming its own engine means one cycle can run both engines, so the
  usage floor is applied per engine: one being out of headroom holds only its
  own candidates.

REVIEW: the engine (codex or claude) receives the main prompt + approval directive +
  post-outcome instructions (review.on_approve / on_comment / on_reject) + any
  matching rule fragments, performs the review itself, posts to GitHub, and
  reports back what it did (APPROVED|COMMENTED|REQUESTED_CHANGES|SKIPPED).
  The tool assumes ONLY the gh CLI plus the selected engine's CLI, which must
  already be authenticated (it never handles engine credentials). Anything else
  (skills, extra CLIs, team conventions) belongs in YOUR prompts, never in
  shipped defaults.

DOCTOR: every external dependency fails LATE and quietly -- a missing or
  logged-out engine CLI surfaces only as repeated ERROR history rows whose
  cause sits in the engine transcript, and a model the permission classifier
  rejects fails identically on every PR. 'doctor' probes them up front (gh +
  auth, duckdb, the configured engine's CLI + auth + settings) and exits
  non-zero on a blocking failure, so it can gate a deploy. serve runs the same
  checks at boot and logs failures rather than refusing to start.

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
  repos ls                 One record per watched repo, with the group its
                           unlisted authors resolve to
  repos add <owner/repo> [--unlisted <group>] [--allowed-authors-only]
    Add a repo (idempotent). --unlisted names the group an author with no
    roster row falls into here, which decides whether strangers are discovered
    at all (a group whose review level is "ignore") and how they are reviewed
    if they are. Left off, an existing setting is preserved.
    --allowed-authors-only is the pre-groups shorthand for "only discover
    rostered authors here". It still works and still reconciles, but --unlisted
    supersedes it and can say more than on/off.
  repos rm <owner/repo>    Stop watching a repo (clears its unlisted setting)

EXAMPLES:
  agent-code-review repos add example-org/example-repo
  agent-code-review repos add example-org/example-repo --unlisted ignored
  agent-code-review repos ls`

const authorsUsageText = `authors: The author roster, i.e. who is in which group (stored in DuckDB)

We are the reviewer. An author belongs to ONE group per repo, and the group is
a complete review policy defined in config under authors.groups:

  review   ignore  | never discovered (a manual queue add still reviews)
           comment | reviewed, never approved
           approve | approvable when the review warrants it
  engine   which agent CLI reviews their PRs (overrides review.engine)
  model    the engine's model for their PRs
  effort   the engine's reasoning effort for their PRs
  prompt   an extra instruction appended to their reviews

approver / commenter / ignored exist without being declared. Only the resolved
policy for the PR under review reaches the engine, never the roster.

RESOLUTION (most specific wins; "authors who" shows which layer decided what):
  1. the roster row for this repo, else the row for "*"
  2. else authors.unlisted[<repo>], else authors.unlisted["*"]
  3. then every matching authors.overrides entry patches it, field by field,
     in config order. Empty inherits; prompt fragments accumulate.

COMMANDS:
  authors ls [--repo <owner/repo|*>] [--group <group>]
    Roster entries with the policy each resolves to. A row naming a group that
    config no longer defines shows up here, resolved as comment-only.

  authors set <owner/repo|*> <github-handle> <group> [--name N] [--email E] [--slack-id S]
    Put an author in a group for a repo (upserts metadata). An undefined group
    is refused here rather than silently resolving to comment-only later.

  authors rm <owner/repo|*> <github-handle>
    Remove a roster entry; the author falls back to authors.unlisted.

  authors groups
    The defined groups, what each grants, and which repos send their unlisted
    authors to it.

  authors who <github-handle> --repo <owner/repo>
    Resolve one author's policy for one repo, with the deciding layer per
    field. This is the answer to "why did that PR get approved / ignored".

EXAMPLES:
  agent-code-review authors set example-org/example-repo example-handle core --name "Example Engineer"
  agent-code-review authors set '*' example-handle contractor   # that group on every repo
  agent-code-review authors who example-handle --repo example-org/example-repo
  agent-code-review authors rm '*' example-handle

NOTES: handles and repos match case-insensitively, as GitHub treats them; group
names match exactly, since they are yours and are validated when you set them.
Self-authored PRs are always comment-only whatever the group grants.`

const promptsUsageText = `prompts: The review prompts (stored in config.json)

The assembled prompt = main prompt + candidate context + built-in approval
directive + post-outcome instructions + matching rules. The engine driver
appends a reporting instruction (final message = JSON verdict) on top.

SLOTS:
  main         The core review instructions
  on-approve   What to do after submitting an approving review
  on-comment   What to do after commenting without approving
  on-reject    What to do after requesting changes
  resume       Nudge sent when a run ends without a final outcome (codex: an
               intermediate WORKING report; claude: no structured report at all)
               (has a built-in default; <engine>.max_resumes bounds the retries)

COMMANDS:
  prompts show                 One record per slot (notes main_prompt_path override)
  prompts set <slot> <text>    Set a slot (multi-word text can be one quoted arg)
  prompts unset <slot>         Clear a slot
  prompts rules ...            Conditional prompt fragments (prompts rules usage)
  prompts preview [--author H] [--group G] [--candidate-type new|refreshed|discussion]
                  [--repo owner/name] [--author-is-gh-user] [--explain]
    Print the fully assembled prompt for a synthetic candidate you shape with
    flags, so any rule can be made to fire. --author names a REAL handle: their
    roster row is read and their per-author overrides fire, so this answers
    "what would this person's PR actually get". --group simulates a membership
    instead, for "what would anyone in this group get". --explain adds two
    traces: how the policy resolved, and each rule's fate.

EXAMPLES:
  agent-code-review prompts set main "Review this PR thoroughly via the gh CLI and leave one review."
  agent-code-review prompts set on-approve "Notify the team channel per our conventions."
  agent-code-review prompts preview --author example-handle --repo example-org/example-repo --explain

NOTES: put workspace-specific conventions (channels, emoji, extra CLIs) in
these slots; the tool itself assumes only the gh and codex CLIs. An
unconditional per-cohort instruction belongs on the GROUP (authors.groups.*
.prompt), not here. Conditional extras (per repo / candidate type / group /
handle), including ones that attach to a specific outcome, are rules; manage
them with 'prompts rules' (prompts rules usage).`

const rulesUsageText = `prompts rules: Conditional prompt fragments (stored in config.json)

A rule adds EXTRA instructions to the assembled prompt when its condition
matches the candidate. Comment-only vs approval is a separate built-in
directive; you never need a rule for it.

Without an --outcome the fragment appends to the prompt body (fires for any
outcome). WITH an --outcome it attaches under that post-outcome section
(approve / comment / reject) alongside the base on_* slot, so behaviour can
branch deterministically, e.g. on the author's group, without relying on
prompt phrasing. Fragments are ADDITIVE: the base slot is the shared part, the
rule carries the conditional part.

An UNCONDITIONAL per-cohort instruction belongs on the group itself
(authors.groups.<name>.prompt), not here. Rules are for what a flat group
prompt cannot express: a fragment for one cohort only on refreshed PRs, or
only under one outcome, or only on one repo.

CONDITIONS (unset = wildcard; all set must hold):
  --group <name>           author's resolved group (repeatable, any-of)
  --author <handle>        author handle (repeatable, any-of, case-insensitive)
  --author-allowed         the resolved policy PERMITS approval
  --author-not-allowed     the resolved policy FORBIDS it (mutually exclusive)
  --author-is-gh-user      self-authored (author == our gh user)
  --author-not-gh-user     NOT self-authored (mutually exclusive with above)
  --candidate-type new|refreshed|discussion
  --repo owner/name        repeatable, any-of
Note: --author-allowed means "the policy permits approval," not "was
approvable": a self-authored PR by an approve-level author is still
comment-only, yet still counts as author-allowed. To split self-review out,
add --author-not-gh-user to those rules and give self-authored PRs their own
--author-is-gh-user rule.

COMMANDS:
  prompts rules ls         One record per rule, in config order (NDJSON)
  prompts rules add --name N --prompt TEXT [--outcome approve|comment|reject] [conditions]
    Append a rule. Name must be unique; a rule with both approval flags, or
    naming an undefined group, is rejected (it could never match).
  prompts rules rm <name>  Remove rule(s) with this name (case-insensitive)

EXAMPLES:
  # On comment, branch the notification reaction on approvability (the
  # channels, emoji, and tooling here are examples; use your own conventions):
  agent-code-review prompts rules add --name comment-not-allowed --outcome comment \
    --author-not-allowed --prompt "React :eyes: on the PR's Slack message."
  agent-code-review prompts rules add --name comment-allowed --outcome comment \
    --author-allowed --prompt "React :memo: on the PR's Slack message."
  # One cohort, only on re-reviews:
  agent-code-review prompts rules add --name contractor-refresh \
    --group contractor --candidate-type refreshed \
    --prompt "Check the previous review's comments were addressed before anything else."
  agent-code-review prompts rules ls

NOTES: put the shared part (e.g. locating the Slack message) in the on-comment
slot via 'prompts set', and the branch-specific part in these rules. See exactly
how they assemble with 'prompts preview [--group <name>] [--explain]'.`

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
  schedule.enabled                     true|false: daemon dispatches reviews
  schedule.interval                    idle poll, e.g. 1m (only when nothing is ready)
  schedule.dispatch_cooldown           pause between dispatches, e.g. 5s (0s disables)
  discovery.enabled                    true|false: daemon scrapes for candidates
  discovery.interval                   scrape cadence, e.g. 10m (gh only, no LLM)
  schedule.max_parallel                1..32 concurrent reviews
  schedule.usage_floor.5h_percent      pause reviews when the engine's 5h usage window
                                       has less than this % remaining (default 10, 0 off)
  schedule.usage_floor.weekly_percent  same for the weekly window (default 10, 0 off)
  candidates.new_max_age_days          New candidate window (default 14)
  candidates.refreshed_max_age_days    Refreshed candidate window (default 21)
  candidates.discussion_max_age_days   Discussion candidate window (default 14)
  candidates.rereview_cooldown         hold after our own review before re-discovery
                                       (default 90m, 0s disables)
  candidates.quiet_period              PR must go untouched this long before discovery
                                       accepts it (default 15m, 0s disables)
  review.engine                        codex (default) | claude
  codex.bin | codex.model | codex.effort | codex.sandbox
  claude.bin | claude.model | claude.effort | claude.permission_mode
  claude.max_budget_usd                per-review ceiling at API rates (0 = uncapped;
                                       set it from the Metrics page's median + peak)
  dashboard.addr                       listen address (default :8330)
  dashboard.tailscale.mode             "" | serve | funnel
  dashboard.usage_poll_interval        engine usage refresh cadence (default 10m)
  store.path                           DuckDB file location

EXAMPLES:
  agent-code-review config set schedule.interval 15m
  agent-code-review config get schedule.interval
  agent-code-review config list

NOTES: repos, authors, and prompts have their own command groups (repos usage,
authors usage, prompts usage); rules live under prompts (prompts rules usage).
codex.args / claude.args / claude.allowed_tools are edited in the file directly
(config path).

ENGINES: both report through the same verdict contract and write the same review
log, so switching review.engine is a one-key change; only that engine's settings
block applies. Usage metering and per-review cost follow the selection too.
  codex   sandboxed 'codex exec'; reports tokens, no cost
  claude  'claude -p', defaults to Opus 5 at medium effort with the permission
          classifier vetting each action; reports tokens AND API-rate cost`

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
