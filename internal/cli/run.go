package cli

import (
	"os"
	"time"

	output "github.com/shhac/lib-agent-output"
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
)

func registerRun(root *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a single review cycle (discover → review → record), then exit",
		Long: "Perform one review cycle and exit. Intended for external schedulers\n" +
			"(launchd/cron) or manual kicks. Honors the same run-lock as the daemon.\n" +
			"Cycle progress logs to stderr; the outcomes recorded during the cycle\n" +
			"are emitted as NDJSON records on stdout, followed by a summary record.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			cfg := config.Read()
			s, err := openStore(cfg)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			// nil usage getter: a manual one-shot run bypasses the usage floor.
			warnf := func(notice, hint string) { output.WriteNotice(os.Stderr, notice, hint) }
			reportConfigProblems(cfg, warnf)
			sched, err := buildScheduler(ctx, config.Read, s, stderrLogf, warnf, nil)
			if err != nil {
				return err
			}
			started := time.Now()
			if err := sched.RunCycle(ctx); err != nil {
				return err
			}

			// The cycle's results are whatever landed in history while it ran
			// (engine verdicts and precheck skips alike): the same rows the
			// History page shows, so stdout carries records, not prose. Under
			// a concurrently-running daemon sharing the store this can include
			// its outcomes too; the run-lock makes that the exception.
			outcomes, err := s.ListReviewsSince(ctx, started)
			if err != nil {
				return err
			}
			if err := emitEach(outcomes, nil); err != nil {
				return err
			}
			return emit(cycleSummary(outcomes, time.Since(started)))
		},
	}
	// --once is accepted for CLI-surface stability but is a no-op: run always
	// performs exactly one cycle. (Registered without a bound variable so it
	// can't read as if it gates behavior.)
	cmd.Flags().Bool("once", true, "Run exactly one cycle (default; currently the only mode)")
	root.AddCommand(cmd)
}

// cycleSummary is the trailing record `run` prints after the outcome rows:
// how long the cycle took and what it produced, bucketed by verdict. Pure, so
// the shape stdout promises is testable without a store or a cycle, matching
// the rest of the repo's extract-the-core convention.
func cycleSummary(outcomes []store.Review, elapsed time.Duration) map[string]any {
	byVerdict := map[string]int{}
	for _, r := range outcomes {
		byVerdict[r.Verdict]++
	}
	return map[string]any{
		"cycle_duration_secs": int(elapsed.Seconds()),
		"outcomes":            len(outcomes),
		"by_verdict":          byVerdict,
	}
}
