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
  serve [--http :8330] [--tailscale serve|funnel]   Run the daemon (scheduler + dashboard)
  run  [--once]                                      Run a single review cycle, then exit

  queue ls [--status S] [--repo R]                   List candidates (NDJSON)
  queue add <owner/repo> <number>                    Add a PR to the queue
  queue rm <owner/repo> <number>                     Remove a PR
  queue promote <owner/repo> <number>                Float a PR to the top
  queue skip <owner/repo> <number>                   Mark a PR skipped

  approvers ls [--repo R]                             List the approver allow-list
  approvers add <owner/repo|*> <handle> [--name ...] Add/update an approver for a repo
  approvers rm <owner/repo|*> <handle>               Remove an approver

  config path | show                                 Config file location / current config
  usage                                              This help

CONFIG: ~/.config/agent-code-review/config.json (respects XDG_CONFIG_HOME).
  Everything tunable lives here — watched repos, the approval allow-list, age
  thresholds (14d New / 21d Refreshed), schedule cadence + parallelism, the
  review engine + main prompt + rules, the DuckDB path, and dashboard/Tailscale.
  See config.example.json. No repos or GitHub handles are hardcoded.

CANDIDATES:
  NEW       — open, not draft, review requested, never reviewed, ≤ new_max_age_days
  REFRESHED — open, not draft, re-review requested, head SHA differs from our last
              recorded review, ≤ refreshed_max_age_days

APPROVAL: the approver allow-list is stored in DuckDB, per repo (manage it with
  'approvers'). A PR author listed for its repo (or the wildcard "*") may be
  approved; anyone else is comment-only. Only this PR's author↔approvable pair is
  passed to the engine — never the whole list.

REVIEW: the engine (codex) receives the main prompt + rule-derived fragments and
  performs the review itself (typically via the pr-issue-review skill), posting to
  GitHub and running any post-approve Slack step. Comment-only enforcement for
  self-authored / non-approvable PRs is expressed as prompt rules, not Go code.

STORE: DuckDB via the duckdb CLI (subprocess, CGO-free). Requires the duckdb
  binary on PATH (brew install duckdb); override with AGENT_CODE_REVIEW_DUCKDB_PATH.

OUTPUT: NDJSON to stdout; errors {error, fixable_by, hint} to stderr, exit 1.`

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
