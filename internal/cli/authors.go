package cli

import (
	"strings"

	output "github.com/shhac/lib-agent-output"
	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
)

func registerAuthors(root *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "authors",
		Short: "Manage the author roster: which group each author is in (stored in DuckDB)",
		Long: "We are the reviewer. An author belongs to one GROUP per repo, and the\n" +
			"group (defined in config under authors.groups) decides what we may do\n" +
			"with their PRs: ignore them, review comment-only, or allow an APPROVE.\n" +
			"A group also carries the engine, model, effort, and extra prompt their\n" +
			"reviews get; authors.overrides narrows any of that per handle.\n\n" +
			"A row for the PR's repo beats a row for the wildcard repo \"*\". An\n" +
			"author with no row resolves through authors.unlisted. Only this PR's\n" +
			"own resolved policy is ever passed to the review engine, never the\n" +
			"whole roster.",
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(authorsLsCmd(), authorsSetCmd(), authorsRmCmd(), authorsGroupsCmd(), authorsWhoCmd())
	registerGroupUsage(cmd, "authors", authorsUsageText)
	root.AddCommand(cmd)
}

func authorsLsCmd() *cobra.Command {
	var repo, group string
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List roster entries with the policy each resolves to (NDJSON)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if repo != "" && !store.ValidAuthorRepo(repo) {
				return invalidAuthorRepo(repo)
			}
			cfg := config.Read()
			return withStore(func(s store.Store) error {
				authors, err := s.ListAuthors(cmd.Context(), repo, group)
				if err != nil {
					return err
				}
				// The resolved policy travels with the row: a row naming a
				// group config no longer defines is invisible otherwise, and
				// this listing is where it should show.
				return emitEach(authors, func(_ int, a store.Author) any {
					policy := cfg.ResolvePolicy(a.Repo, a.GitHubHandle,
						config.Membership{Group: a.Group, Repo: a.Repo})
					return struct {
						store.Author
						Policy config.Policy `json:"policy"`
					}{Author: a, Policy: policy}
				})
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&repo, "repo", "", `Filter by repo ("owner/name" or "*")`)
	f.StringVar(&group, "group", "", "Filter by group")
	_ = cmd.RegisterFlagCompletionFunc("repo", completeAuthorRepo)
	_ = cmd.RegisterFlagCompletionFunc("group", completeGroup)
	return cmd
}

func authorsSetCmd() *cobra.Command {
	var name, email, slackID string
	cmd := &cobra.Command{
		Use:   "set <owner/repo|*> <github-handle> <group>",
		Short: "Put an author in a group for a repo (upserts)",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, handle, group := args[0], args[1], args[2]
			if !store.ValidAuthorRepo(repo) {
				return invalidAuthorRepo(repo)
			}
			cfg := config.Read()
			// Refuse a group nobody defined. The resolver treats an unknown
			// group as comment-only rather than failing a review, which is the
			// right behaviour at review time; at write time it is a typo and
			// should be caught while the person is still looking at it.
			if _, ok := cfg.Group(group); !ok {
				return output.New("Unknown group "+group+". Valid: "+strings.Join(cfg.GroupNames(), ", ")+
					". Define new groups under authors.groups in config.json", output.FixableByAgent)
			}
			return withStore(func(s store.Store) error {
				a := store.Author{
					Repo:         repo,
					GitHubHandle: handle,
					Group:        group,
					Name:         name,
					Email:        email,
					SlackID:      slackID,
				}
				if err := s.SetAuthorGroup(cmd.Context(), a); err != nil {
					return err
				}
				return emit(map[string]any{
					"set":    a.Repo + " / @" + a.GitHubHandle,
					"group":  group,
					"policy": cfg.ResolvePolicy(a.Repo, a.GitHubHandle, config.Membership{Group: group, Repo: a.Repo}),
				})
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "Display name")
	f.StringVar(&email, "email", "", "Email")
	f.StringVar(&slackID, "slack-id", "", "Slack user ID")
	cmd.ValidArgsFunction = completePositional(completeAuthorRepo, completeAuthorHandle, completeGroupArg)
	return cmd
}

func authorsRmCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rm <owner/repo|*> <github-handle>",
		Short: "Remove a roster entry (the author falls back to authors.unlisted)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !store.ValidAuthorRepo(args[0]) {
				return invalidAuthorRepo(args[0])
			}
			cfg := config.Read()
			return withStore(func(s store.Store) error {
				if err := s.RemoveAuthor(cmd.Context(), args[0], args[1]); err != nil {
					return err
				}
				return emit(map[string]any{
					"removed":  args[0] + " / @" + args[1],
					"now_gets": cfg.UnlistedPolicy(args[0]),
				})
			})
		},
	}
	cmd.ValidArgsFunction = completePositional(completeAuthorRepo, completeAuthorHandle)
	return cmd
}

// authorsGroupsCmd lists the cohorts a roster entry can point at. Group
// definitions are config, so this is a read-only view of them; editing happens
// in config.json, where the prompts they carry belong.
func authorsGroupsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "groups",
		Short: "List the defined groups and what each one grants (NDJSON)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg := config.Read()
			names := cfg.GroupNames()
			return emitEach(names, func(_ int, name string) any {
				g, _ := cfg.Group(name)
				_, declared := cfg.Authors.Groups[name]
				review := g.Review
				if review == "" {
					review = config.ReviewComment
				}
				return map[string]any{
					"group":    name,
					"review":   review,
					"engine":   g.Engine,
					"model":    g.Model,
					"effort":   g.Effort,
					"prompt":   g.Prompt,
					"builtin":  !declared,
					"unlisted": unlistedRepos(cfg, name),
				}
			})
		},
	}
}

// authorsWhoCmd answers the question the cascade exists to make answerable:
// what does THIS author get on THIS repo, and which layer decided each part.
func authorsWhoCmd() *cobra.Command {
	var repo string
	cmd := &cobra.Command{
		Use:   "who <github-handle>",
		Short: "Resolve one author's policy for a repo, with the deciding layer per field",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if repo == "" {
				return output.New("--repo is required: a policy is resolved per repo", output.FixableByAgent)
			}
			if !store.ValidAuthorRepo(repo) {
				return invalidAuthorRepo(repo)
			}
			cfg := config.Read()
			return withStore(func(s store.Store) error {
				membership, err := s.AuthorGroup(cmd.Context(), repo, args[0])
				if err != nil {
					return err
				}
				policy, trace := cfg.ExplainPolicy(repo, args[0], membership)
				return emit(map[string]any{
					"author": args[0],
					"repo":   repo,
					"policy": policy,
					"trace":  trace,
				})
			})
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", `Repo to resolve for ("owner/name")`)
	_ = cmd.RegisterFlagCompletionFunc("repo", completeRepos)
	cmd.ValidArgsFunction = completePositional(completeAnyAuthorHandle)
	return cmd
}

// unlistedRepos names the repos whose unlisted authors land in this group, so
// `authors groups` shows the reach of a cohort nobody is explicitly rostered
// into.
func unlistedRepos(cfg config.Config, group string) []string {
	var repos []string
	for _, repo := range append([]string{config.WildcardRepo}, cfg.SortedRepos()...) {
		if cfg.UnlistedPolicy(repo).Group == group {
			repos = append(repos, repo)
		}
	}
	return repos
}

func invalidAuthorRepo(repo string) error {
	return output.New(`Repo must be owner/name or "*", got `+repo, output.FixableByAgent)
}
