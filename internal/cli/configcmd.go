package cli

import (
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
)

func registerConfig(root *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect configuration",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "path",
			Short: "Print the config file path",
			Args:  cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				return emit(map[string]string{"path": config.Path()})
			},
		},
		&cobra.Command{
			Use:   "show",
			Short: "Print the current resolved config",
			Args:  cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				return emit(config.Read())
			},
		},
	)
	root.AddCommand(cmd)
}
