package cli

import (
	"strings"

	output "github.com/shhac/lib-agent-output"
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
)

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
			return emitEach(cfg.SortedRepos(), func(_ int, r string) any {
				// The resolved answer, not the raw setting: an author with no
				// roster row on this repo gets this group, whether that came
				// from authors.unlisted or the legacy repo list.
				unlisted := cfg.UnlistedPolicy(r)
				return map[string]any{
					"repo":            r,
					"unlisted_group":  unlisted.Group,
					"unlisted_review": unlisted.Review,
				}
			})
		},
	}
}

func reposAddCmd() *cobra.Command {
	var allowedOnly bool
	var unlisted string
	cmd := &cobra.Command{
		Use:   "add <owner/repo> [--unlisted group]",
		Short: "Add a repo to the watch list",
		Long: "Watch a repo for candidate PRs.\n\n" +
			"--unlisted names the group an author with no roster row falls into\n" +
			"here, which is what decides whether strangers are discovered at all\n" +
			"(a group whose review level is \"ignore\") and how they are reviewed if\n" +
			"they are. Left alone, an existing setting is preserved.\n\n" +
			"--allowed-authors-only is the pre-groups shorthand for \"discover only\n" +
			"rostered authors here\". It still works and still reconciles, but\n" +
			"--unlisted supersedes it and can say more than on/off.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			repo := args[0]
			if !config.ValidRepoName(repo) {
				return invalidRepo(repo)
			}
			cfgNow := config.Read()
			if unlisted != "" {
				if _, ok := cfgNow.Group(unlisted); !ok {
					return output.New("Unknown group "+unlisted+". Valid: "+
						strings.Join(cfgNow.GroupNames(), ", "), output.FixableByAgent)
				}
			}
			watched := false
			if err := config.Update(func(cfg *config.Config) error {
				watched = cfg.WatchesRepo(repo)
				if !watched {
					cfg.Repos = append(cfg.Repos, repo)
				}
				if unlisted != "" {
					if cfg.Authors.Unlisted == nil {
						cfg.Authors.Unlisted = map[string]string{}
					}
					cfg.Authors.Unlisted[repo] = unlisted
				}
				// Reconcile the legacy scope list with its flag (add or remove
				// membership), unchanged: it is the only thing that flag has
				// ever managed, so a config using it keeps behaving the same.
				scoped := cfg.AuthorScopedRepo(repo)
				if allowedOnly && !scoped {
					cfg.AllowedAuthorsOnlyRepos = append(cfg.AllowedAuthorsOnlyRepos, repo)
				} else if !allowedOnly && scoped {
					cfg.AllowedAuthorsOnlyRepos, _ = filterFold(cfg.AllowedAuthorsOnlyRepos, self, repo)
				}
				return nil
			}); err != nil {
				return err
			}
			verb := "added"
			if watched {
				verb = "updated"
			}
			resolved := config.Read().UnlistedPolicy(repo)
			return emit(map[string]any{
				verb:              repo,
				"unlisted_group":  resolved.Group,
				"unlisted_review": resolved.Review,
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&unlisted, "unlisted", "",
		"Group an author with no roster row falls into on this repo")
	f.BoolVar(&allowedOnly, "allowed-authors-only", false,
		"Pre-groups shorthand: only discover PRs from rostered authors here")
	_ = cmd.RegisterFlagCompletionFunc("unlisted", completeGroup)
	return cmd
}

// invalidRepo is the shared "owner/name" validation error, mirroring
// invalidAuthorRepo (which also accepts the "*" wildcard). Used by every
// command that takes a plain repo argument or --repo flag.
func invalidRepo(repo string) error {
	return output.New("Repo must be owner/name, got "+repo, output.FixableByAgent)
}

func reposRmCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rm <owner/repo>",
		Short: "Remove a repo from the watch list",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			repo := args[0]
			if err := config.Update(func(cfg *config.Config) error {
				kept, removed := filterFold(cfg.Repos, self, repo)
				if removed == 0 {
					return output.New("Not a watched repo: "+repo, output.FixableByAgent).
						WithHint("run 'agent-code-review repos ls' to see the watch list")
				}
				cfg.Repos = kept
				cfg.AllowedAuthorsOnlyRepos, _ = filterFold(cfg.AllowedAuthorsOnlyRepos, self, repo)
				// Both ways of scoping a repo's unlisted authors go with it;
				// leaving either behind would silently reapply if the repo is
				// ever re-added.
				delete(cfg.Authors.Unlisted, repo)
				return nil
			}); err != nil {
				return err
			}
			return emit(map[string]any{"removed": repo})
		},
	}
	cmd.ValidArgsFunction = completeRepos
	return cmd
}
