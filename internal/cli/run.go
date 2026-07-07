package cli

import (
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
)

func registerRun(root *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a single review cycle (discover → review → record), then exit",
		Long: "Perform one review cycle and exit. Intended for external schedulers\n" +
			"(launchd/cron) or manual kicks. Honors the same run-lock as the daemon.",
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
			sched, err := buildScheduler(ctx, cfg, s, stderrLogf, nil)
			if err != nil {
				return err
			}
			return sched.RunCycle(ctx)
		},
	}
	// --once is accepted for CLI-surface stability but is a no-op: run always
	// performs exactly one cycle. (Registered without a bound variable so it
	// can't read as if it gates behavior.)
	cmd.Flags().Bool("once", true, "Run exactly one cycle (default; currently the only mode)")
	root.AddCommand(cmd)
}
