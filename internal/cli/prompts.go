package cli

import (
	"strings"

	output "github.com/shhac/lib-agent-output"
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
)

// promptSlots maps slot names to accessors on ReviewSettings. "main" is the
// core review prompt; the on-* slots are post-outcome instructions.
var promptSlots = []string{"main", "on-approve", "on-comment", "on-reject"}

func slotAccess(r *config.ReviewSettings, slot string) (get func() string, set func(string)) {
	switch slot {
	case "main":
		return func() string { return r.MainPrompt }, func(v string) { r.MainPrompt = v }
	case "on-approve":
		return func() string { return r.OnApprove }, func(v string) { r.OnApprove = v }
	case "on-comment":
		return func() string { return r.OnComment }, func(v string) { r.OnComment = v }
	case "on-reject":
		return func() string { return r.OnReject }, func(v string) { r.OnReject = v }
	default:
		return nil, nil
	}
}

func unknownSlotError(slot string) error {
	return output.New("Unknown prompt slot: "+slot+". Valid: "+strings.Join(promptSlots, ", "), output.FixableByAgent)
}

func registerPrompts(root *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "prompts",
		Short: "Inspect and edit the review prompts (stored in config.json)",
		Long: "The prompts handed to the review agent: the main prompt plus the\n" +
			"post-outcome instructions (on-approve / on-comment / on-reject). The\n" +
			"approval directive and rules also feed the assembled prompt — see\n" +
			"'prompts preview' for exactly what the agent receives.",
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(promptsShowCmd(), promptsSetCmd(), promptsUnsetCmd(), promptsPreviewCmd())
	registerGroupUsage(cmd, "prompts", promptsUsageText)
	root.AddCommand(cmd)
}

func promptsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the configured prompts (one record per slot)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg := config.Read()
			for _, slot := range promptSlots {
				get, _ := slotAccess(&cfg.Review, slot)
				rec := map[string]any{"slot": slot, "value": get()}
				if slot == "main" && cfg.Review.MainPromptPath != "" {
					rec["overridden_by"] = "main_prompt_path: " + cfg.Review.MainPromptPath
					rec["effective"] = review.MainPrompt(cfg.Review)
				}
				if err := emit(rec); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func promptsSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <main|on-approve|on-comment|on-reject> <text>",
		Short: "Set a prompt slot",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			slot, text := args[0], strings.TrimSpace(strings.Join(args[1:], " "))
			cfg := config.Read()
			_, set := slotAccess(&cfg.Review, slot)
			if set == nil {
				return unknownSlotError(slot)
			}
			if slot == "main" && cfg.Review.MainPromptPath != "" {
				return output.New("main_prompt_path is set ("+cfg.Review.MainPromptPath+") and overrides main_prompt", output.FixableByHuman).
					WithHint("edit that file instead, or clear main_prompt_path in config.json first")
			}
			set(text)
			if err := config.Write(cfg); err != nil {
				return err
			}
			return emit(map[string]any{"slot": slot, "value": text})
		},
	}
}

func promptsUnsetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unset <main|on-approve|on-comment|on-reject>",
		Short: "Clear a prompt slot",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			slot := args[0]
			cfg := config.Read()
			_, set := slotAccess(&cfg.Review, slot)
			if set == nil {
				return unknownSlotError(slot)
			}
			set("")
			if err := config.Write(cfg); err != nil {
				return err
			}
			return emit(map[string]any{"slot": slot, "value": ""})
		},
	}
}

func promptsPreviewCmd() *cobra.Command {
	var notAllowed bool
	cmd := &cobra.Command{
		Use:   "preview",
		Short: "Print the fully assembled prompt for a synthetic candidate",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := config.Read()
			sample := store.Candidate{
				Repo:    "example-org/example-repo",
				Number:  123,
				Type:    store.TypeNew,
				Author:  "example-author",
				URL:     "https://github.com/example-org/example-repo/pull/123",
				HeadSHA: "0000000000000000000000000000000000000000",
			}
			prompt := review.BuildPrompt(cfg, sample, review.Facts{AuthorAllowed: !notAllowed})
			return emit(map[string]any{
				"variant": map[bool]string{false: "allowed_author", true: "not_allowed_author"}[notAllowed],
				"preview": prompt,
				"note":    "synthetic candidate; the engine driver appends a reporting instruction on top",
			})
		},
	}
	cmd.Flags().BoolVar(&notAllowed, "author-not-allowed", false, "Preview the comment-only variant")
	return cmd
}
