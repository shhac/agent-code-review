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
			Use:   "init",
			Short: "Write an annotated starter config (refuses to overwrite)",
			Args:  cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				path, err := config.Init()
				if err != nil {
					return err
				}
				return emit(map[string]string{"created": path, "next": "add repos via 'repos add', allow authors via 'authors allow', then tune review.main_prompt and schedule in the file"})
			},
		},
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
