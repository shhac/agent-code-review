package cli

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// The reference cards are prose, kept as plain files under usage/ so they
// read and diff as documents rather than as one Go string literal apiece.
// Each prints through strings.TrimSpace, so a file's trailing newline is not
// part of the output.
var (
	//go:embed usage/root.txt
	usageText string
	//go:embed usage/queue.txt
	queueUsageText string
	//go:embed usage/repos.txt
	reposUsageText string
	//go:embed usage/authors.txt
	authorsUsageText string
	//go:embed usage/score.txt
	scoreUsageText string
	//go:embed usage/prompts.txt
	promptsUsageText string
	//go:embed usage/rules.txt
	rulesUsageText string
	//go:embed usage/config.txt
	configUsageText string
)

func registerUsage(root *cobra.Command) {
	root.AddCommand(&cobra.Command{
		Use:   "usage",
		Short: "Print concise documentation (LLM-optimized)",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace(usageText))
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
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace(text))
		},
	})
}
