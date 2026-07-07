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
  serve [--http :8330] [--tailscale serve|funnel]   Run the daemon (scheduler + dashboard:
                                                     Overview w/ queue add+reorder, Config, Prompt)
  run  [--once]                                      Run a single review cycle, then exit

  queue ls [--status S] [--repo R]                   List candidates (NDJSON)
  queue add <owner/repo> <number>                    Add a PR to the queue
  queue rm <owner/repo> <number>                     Remove a PR
  queue promote <owner/repo> <number>                Float a PR to the top
  queue skip <owner/repo> <number>                   Mark a PR skipped

  repos ls | add <owner/repo> | rm <owner/repo>      Manage the watched repos (config)

  authors ls [--repo R]                              List allowed authors
  authors allow <owner/repo|*> <handle> [--name ...] Allow an author's PRs to be approved
  authors deny <owner/repo|*> <handle>               Make an author's PRs comment-only again

  config init                                        Write an annotated starter config
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
