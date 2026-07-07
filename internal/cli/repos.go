package cli

import (
	"regexp"
	"strings"

	output "github.com/shhac/lib-agent-output"
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
)

var repoArgPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func registerRepos(root *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "repos",
		Short: "Manage the watched repos (stored in config.json)",
		Long: "The repos this tool discovers candidate PRs in. Discovery, the dashboard\n" +
			"add-PR form, and the scheduler all operate only on this list.",
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(reposLsCmd(), reposAddCmd(), reposRmCmd())
	registerGroupUsage(cmd, "repos", reposUsageText)
	root.AddCommand(cmd)
}

func reposLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List watched repos (NDJSON)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			for _, r := range config.Read().Repos {
				if err := emit(map[string]string{"repo": r}); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func reposAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <owner/repo>",
		Short: "Add a repo to the watch list",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			repo := args[0]
			if !repoArgPattern.MatchString(repo) {
				return output.New("Repo must be owner/name, got "+repo, output.FixableByAgent)
			}
			cfg := config.Read()
			for _, r := range cfg.Repos {
				if strings.EqualFold(r, repo) {
					return emit(map[string]any{"unchanged": repo, "reason": "already watched"})
				}
			}
			cfg.Repos = append(cfg.Repos, repo)
			if err := config.Write(cfg); err != nil {
				return err
			}
			return emit(map[string]any{"added": repo})
		},
	}
}

func reposRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <owner/repo>",
		Short: "Remove a repo from the watch list",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			repo := args[0]
			cfg := config.Read()
			kept := cfg.Repos[:0]
			found := false
			for _, r := range cfg.Repos {
				if strings.EqualFold(r, repo) {
					found = true
					continue
				}
				kept = append(kept, r)
			}
			if !found {
				return output.New("Not a watched repo: "+repo, output.FixableByAgent).
					WithHint("run 'agent-code-review repos ls' to see the watch list")
			}
			cfg.Repos = kept
			if err := config.Write(cfg); err != nil {
				return err
			}
			return emit(map[string]any{"removed": repo})
		},
	}
}
