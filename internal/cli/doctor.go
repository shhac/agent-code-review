package cli

import (
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/doctor"
	output "github.com/shhac/lib-agent-output"
)

func registerDoctor(root *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check whether this machine can run a review",
		Long: "Probe every external dependency a review needs: the gh CLI and its\n" +
			"auth, the duckdb store binary, and the configured engine's CLI, auth,\n" +
			"and settings. Each of these otherwise fails only at review time, as an\n" +
			"ERROR history row whose cause is buried in the engine transcript.\n\n" +
			"Exits non-zero when a blocking check fails, so it can gate a deploy.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			checks := doctor.Run(cmd.Context(), config.Read())
			for _, c := range checks {
				if err := emit(c); err != nil {
					return err
				}
			}
			failed := doctor.Blocking(checks)
			if len(failed) == 0 {
				return nil
			}
			// Name the first failure in the error: the per-check lines carry
			// the detail, but the exit needs a reason a human reads first.
			return output.Newf(output.FixableByHuman,
				"%d blocking check(s) failed, starting with %s: %s. Hint: %s",
				len(failed), failed[0].Name, failed[0].Detail, failed[0].Hint)
		},
	}
	root.AddCommand(cmd)
}
