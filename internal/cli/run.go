package cli

import (
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
)

func registerRun(root *cobra.Command) {
	var once bool
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

			sched, err := buildScheduler(ctx, cfg, s)
			if err != nil {
				return err
			}
			return sched.RunCycle(ctx)
		},
	}
	// --once is the default and only mode today; the flag documents intent and
	// leaves room for a future --watch.
	cmd.Flags().BoolVar(&once, "once", true, "Run exactly one cycle (default)")
	root.AddCommand(cmd)
}
