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
// core review prompt; the on-* slots are post-outcome instructions; "resume"
// is the nudge sent when a run ends on an intermediate WORKING report.
var promptSlots = []string{"main", "on-approve", "on-comment", "on-reject", "resume"}

// slotField returns a pointer to the ReviewSettings field backing a prompt
// slot, or nil for an unknown slot. Read with *p, write with *p = v.
func slotField(r *config.ReviewSettings, slot string) *string {
	switch slot {
	case "main":
		return &r.MainPrompt
	case "on-approve":
		return &r.OnApprove
	case "on-comment":
		return &r.OnComment
	case "on-reject":
		return &r.OnReject
	case "resume":
		return &r.ResumePrompt
	default:
		return nil
	}
}

func unknownSlotError(slot string) error {
	return output.New("Unknown prompt slot: "+slot+". Valid: "+strings.Join(promptSlots, ", "), output.FixableByAgent)
}

func registerPrompts(root *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "prompts",
		Short: "Inspect and edit the review prompts (stored in config.json)",
		Long: "The prompts handed to the review agent: the main prompt, the\n" +
			"post-outcome instructions (on-approve / on-comment / on-reject), and\n" +
			"the resume nudge sent when a run ends on a WORKING report. The\n" +
			"approval directive and rules also feed the assembled prompt; see\n" +
			"'prompts preview' for exactly what the agent receives.",
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(promptsShowCmd(), promptsSetCmd(), promptsUnsetCmd(), promptsPreviewCmd(), rulesCmd())
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
				rec := map[string]any{"slot": slot, "value": *slotField(&cfg.Review, slot)}
				if slot == "main" && cfg.Review.MainPromptPath != "" {
					rec["overridden_by"] = "main_prompt_path: " + cfg.Review.MainPromptPath
					rec["effective"] = review.MainPrompt(cfg.Review)
				}
				if slot == "resume" && cfg.Review.ResumePrompt == "" {
					rec["effective"] = review.ResumePrompt(cfg.Review)
					rec["note"] = "built-in default; override with 'prompts set resume'"
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
	cmd := &cobra.Command{
		Use:   "set <main|on-approve|on-comment|on-reject|resume> <text>",
		Short: "Set a prompt slot",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			slot, text := args[0], strings.TrimSpace(strings.Join(args[1:], " "))
			if err := config.Update(func(cfg *config.Config) error {
				p := slotField(&cfg.Review, slot)
				if p == nil {
					return unknownSlotError(slot)
				}
				if slot == "main" && cfg.Review.MainPromptPath != "" {
					return output.New("main_prompt_path is set ("+cfg.Review.MainPromptPath+") and overrides main_prompt", output.FixableByHuman).
						WithHint("edit that file instead, or clear main_prompt_path in config.json first")
				}
				*p = text
				return nil
			}); err != nil {
				return err
			}
			return emit(map[string]any{"slot": slot, "value": text})
		},
	}
	cmd.ValidArgsFunction = func(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return noFile(completePrefix(promptSlots, toComplete))
		}
		return noFile(nil)
	}
	return cmd
}

func promptsUnsetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unset <main|on-approve|on-comment|on-reject|resume>",
		Short: "Clear a prompt slot",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			slot := args[0]
			if err := config.Update(func(cfg *config.Config) error {
				p := slotField(&cfg.Review, slot)
				if p == nil {
					return unknownSlotError(slot)
				}
				*p = ""
				return nil
			}); err != nil {
				return err
			}
			return emit(map[string]any{"slot": slot, "value": ""})
		},
	}
	cmd.ValidArgsFunction = func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return noFile(completePrefix(promptSlots, toComplete))
	}
	return cmd
}

func promptsPreviewCmd() *cobra.Command {
	var (
		notAllowed, isGHUser, explain bool
		candidateType, repo           string
	)
	cmd := &cobra.Command{
		Use:   "preview [--author-not-allowed] [--candidate-type new|refreshed] [--repo owner/name] [--author-is-gh-user] [--explain]",
		Short: "Print the fully assembled prompt for a synthetic candidate",
		Long: "Assemble the exact prompt the engine would receive for a synthetic\n" +
			"candidate you shape with flags, so any rule (by allow-list, repo, type,\n" +
			"or self-authorship) can be made to fire. --explain adds a per-rule trace\n" +
			"of what matched and why, without you having to read the assembled text.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if candidateType == "" {
				candidateType = store.TypeNew
			}
			if !config.ValidCandidateType(candidateType) {
				return output.New("--candidate-type must be one of "+strings.Join(config.CandidateTypes, ", ")+", got "+candidateType, output.FixableByAgent)
			}
			if repo == "" {
				repo = review.SampleRepo
			}
			if !config.ValidRepoName(repo) {
				return invalidRepo(repo)
			}
			cfg := config.Read()
			sample := review.SampleCandidate(repo, candidateType)
			facts := review.Facts{AuthorAllowed: !notAllowed, AuthorIsGHUser: isGHUser}
			variant := "allowed_author"
			if notAllowed {
				variant = "not_allowed_author"
			}
			rec := map[string]any{
				"variant": variant,
				"candidate": map[string]any{
					"repo":              sample.Repo,
					"candidate_type":    sample.Type,
					"author_allowed":    facts.AuthorAllowed,
					"author_is_gh_user": facts.AuthorIsGHUser,
				},
				"preview": review.BuildPrompt(cfg, sample, facts),
				"note":    "synthetic candidate; the engine driver appends a reporting instruction on top",
			}
			if explain {
				rec["rules"] = review.ExplainRules(cfg, sample, facts)
			}
			return emit(rec)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&notAllowed, "author-not-allowed", false, "Author is NOT on the allowed-authors list (default: allowed)")
	f.BoolVar(&isGHUser, "author-is-gh-user", false, "Author is our gh user (self-authored)")
	f.StringVar(&candidateType, "candidate-type", "", "Candidate kind: new (default) | refreshed")
	f.StringVar(&repo, "repo", "", "Repo the synthetic candidate belongs to (default: example-org/example-repo)")
	f.BoolVar(&explain, "explain", false, "Also trace which rules fired and why")
	_ = cmd.RegisterFlagCompletionFunc("candidate-type", completeStatic(config.CandidateTypes))
	_ = cmd.RegisterFlagCompletionFunc("repo", completeRepos)
	return cmd
}
