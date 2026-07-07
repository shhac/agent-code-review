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
			cfg := config.Read()
			for _, r := range cfg.Repos {
				scope := "any"
				if cfg.AuthorScopedRepo(r) {
					scope = "allowed-authors-only"
				}
				if err := emit(map[string]string{"repo": r, "authors": scope}); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func reposAddCmd() *cobra.Command {
	var allowedOnly bool
	cmd := &cobra.Command{
		Use:   "add <owner/repo>",
		Short: "Add a repo to the watch list",
		Long: "Watch a repo for candidate PRs. By default any open PR is discovered\n" +
			"(the allowed-authors list then only governs approve vs comment-only);\n" +
			"--allowed-authors-only scopes discovery itself to allowed authors — for\n" +
			"repos where reviewing every PR would be noise.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			repo := args[0]
			if !repoArgPattern.MatchString(repo) {
				return output.New("Repo must be owner/name, got "+repo, output.FixableByAgent)
			}
			cfg := config.Read()
			watched := false
			for _, r := range cfg.Repos {
				if strings.EqualFold(r, repo) {
					watched = true
					break
				}
			}
			if !watched {
				cfg.Repos = append(cfg.Repos, repo)
			}
			// Reconcile the scope list with the flag (add or remove membership).
			scoped := cfg.AuthorScopedRepo(repo)
			if allowedOnly && !scoped {
				cfg.AllowedAuthorsOnlyRepos = append(cfg.AllowedAuthorsOnlyRepos, repo)
			} else if !allowedOnly && scoped {
				cfg.AllowedAuthorsOnlyRepos = removeFold(cfg.AllowedAuthorsOnlyRepos, repo)
			}
			if err := config.Write(cfg); err != nil {
				return err
			}
			scope := "any"
			if allowedOnly {
				scope = "allowed-authors-only"
			}
			verb := "added"
			if watched {
				verb = "updated"
			}
			return emit(map[string]any{verb: repo, "authors": scope})
		},
	}
	cmd.Flags().BoolVar(&allowedOnly, "allowed-authors-only", false,
		"Only discover PRs authored by allowed authors in this repo")
	return cmd
}

// removeFold returns list without repo (case-insensitive).
func removeFold(list []string, repo string) []string {
	kept := list[:0]
	for _, r := range list {
		if !strings.EqualFold(r, repo) {
			kept = append(kept, r)
		}
	}
	return kept
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
			cfg.AllowedAuthorsOnlyRepos = removeFold(cfg.AllowedAuthorsOnlyRepos, repo)
			if err := config.Write(cfg); err != nil {
				return err
			}
			return emit(map[string]any{"removed": repo})
		},
	}
}
