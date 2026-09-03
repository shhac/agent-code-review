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
	var ignoreFloor bool
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Discover, drain the review queue, then exit",
		Long: "Perform one discovery sweep, review everything the queue makes ready,\n" +
			"and exit. Intended for external schedulers (launchd/cron) or manual\n" +
			"kicks. Progress logs to stderr; the outcomes recorded along the way are\n" +
			"emitted as NDJSON records on stdout, followed by a summary record.\n\n" +
			"There is no global run-lock: running this against a live daemon is safe\n" +
			"(each PR is claimed by exactly one reviewer, store-wide) but the two do\n" +
			"compete for the queue, and stdout may then carry the daemon's outcomes\n" +
			"as well as this run's.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			cfg := config.Read()
			s, err := openStore(cfg)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			// The usage floor applies here exactly as it does in the daemon.
			// It used to be bypassed, which was survivable only because the
			// run-lock made an overlapping cron run a no-op: without that, a
			// scheduled run would spend from an account the daemon had
			// deliberately parked at its floor.
			warnf := func(notice, hint string) { output.WriteNotice(os.Stderr, notice, hint) }
			reportConfigProblems(cfg, warnf)
			usageFn := oneShotUsage(ctx, cfg)
			if ignoreFloor {
				usageFn = nil
			}
			sched, err := buildScheduler(ctx, config.Read, s, stderrLogf, warnf, usageFn)
			if err != nil {
				return err
			}
			started := time.Now()
			if err := sched.RunOnce(ctx); err != nil {
				return err
			}

			// The results are whatever landed in history while this ran
			// (engine verdicts and precheck skips alike): the same rows the
			// History page shows, so stdout carries records, not prose. A
			// concurrently-running daemon sharing the store contributes its
			// outcomes here too.
			outcomes, err := s.ListReviewsSince(ctx, started)
			if err != nil {
				return err
			}
			if err := emitEach(outcomes, nil); err != nil {
				return err
			}
			return emit(runSummary(outcomes, time.Since(started)))
		},
	}
	// --once is accepted for CLI-surface stability but is a no-op: run always
	// drains once and exits. (Registered without a bound variable so it can't
	// read as if it gates behavior.)
	cmd.Flags().Bool("once", true, "Drain the queue once (default; currently the only mode)")
	cmd.Flags().BoolVar(&ignoreFloor, "ignore-usage-floor", false,
		"Review even when an engine is below its usage floor (the daemon never does)")
	root.AddCommand(cmd)
}

// runSummary is the trailing record `run` prints after the outcome rows: how
// long the drain took and what it produced, bucketed by verdict. Pure, so the
// shape stdout promises is testable without a store or a scheduler, matching
// the rest of the repo's extract-the-core convention.
func runSummary(outcomes []store.Review, elapsed time.Duration) map[string]any {
	byVerdict := map[string]int{}
	for _, r := range outcomes {
		byVerdict[r.Verdict]++
	}
	return map[string]any{
		"duration_secs": int(elapsed.Seconds()),
		"outcomes":      len(outcomes),
		"by_verdict":    byVerdict,
	}
}
