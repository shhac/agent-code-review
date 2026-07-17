package cli

import (
	"context"
	"encoding/json"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
)

const completionTimeout = 2 * time.Second

func noFile(values []string) ([]string, cobra.ShellCompDirective) {
	return values, cobra.ShellCompDirectiveNoFileComp
}

func completePrefix(values []string, prefix string) []string {
	seen := make(map[string]struct{}, len(values))
	var out []string
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			if _, ok := seen[value]; !ok {
				seen[value] = struct{}{}
				out = append(out, value)
			}
		}
	}
	sort.Strings(out)
	return out
}

// completeStatic completes against a fixed value set (enum flags, config keys).
// The canonical shape for "suggest one of these, no file completion".
func completeStatic(values []string) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return noFile(completePrefix(values, toComplete))
	}
}

func attachConfigCompletions(cmd *cobra.Command, specs []configKeySpec) {
	keyNames := make([]string, 0, len(specs))
	for _, spec := range specs {
		keyNames = append(keyNames, spec.key.Name)
	}
	for _, verb := range []string{"get", "unset"} {
		findCommand(cmd, verb).ValidArgsFunction = completeStatic(keyNames)
	}
	findCommand(cmd, "set").ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return noFile(completePrefix(keyNames, toComplete))
		}
		if len(args) == 1 {
			return noFile(completeConfigValue(cmd.Context(), args[0], toComplete))
		}
		return noFile(nil)
	}
}

func completeConfigValue(ctx context.Context, key, prefix string) []string {
	for _, spec := range configKeySpecs() {
		if spec.key.Name == key && spec.complete != nil {
			return completePrefix(spec.complete(ctx), prefix)
		}
	}
	return nil
}

func completeConfiguredCodexEfforts(ctx context.Context) []string {
	return codexModelEfforts(ctx, config.Read().Review.Codex.Model)
}

func findCommand(parent *cobra.Command, name string) *cobra.Command {
	for _, child := range parent.Commands() {
		if child.Name() == name {
			return child
		}
	}
	return nil
}

type codexModel struct {
	Slug                     string `json:"slug"`
	SupportedReasoningLevels []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
}

func codexModels(ctx context.Context) ([]codexModel, error) {
	bin := config.Read().Review.Codex.Bin
	if bin == "" {
		bin = "codex"
	}
	ctx, cancel := context.WithTimeout(ctx, completionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "debug", "models").Output()
	if err != nil {
		return nil, err
	}
	return parseCodexModels(out)
}

func parseCodexModels(data []byte) ([]codexModel, error) {
	var response struct {
		Models []codexModel `json:"models"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	return response.Models, nil
}

func codexModelSlugs(ctx context.Context) []string {
	models, err := codexModels(ctx)
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(models))
	for _, model := range models {
		values = append(values, model.Slug)
	}
	return values
}

func codexModelEfforts(ctx context.Context, modelSlug string) []string {
	models, err := codexModels(ctx)
	if err != nil {
		return nil
	}
	var values []string
	for _, model := range models {
		if modelSlug != "" && model.Slug != modelSlug {
			continue
		}
		for _, level := range model.SupportedReasoningLevels {
			values = append(values, level.Effort)
		}
	}
	return values
}

func completeRepos(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return noFile(completePrefix(config.Read().SortedRepos(), toComplete))
}

func completeRuleNames(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	rules := config.Read().Review.Rules
	names := make([]string, 0, len(rules))
	for _, r := range rules {
		if r.Name != "" {
			names = append(names, r.Name)
		}
	}
	return noFile(completePrefix(names, toComplete))
}

func completionStore() (store.Store, error) {
	cfg := config.Read()
	return store.Open(cfg.Store.Engine, cfg.StorePath())
}

// completeFromStore opens the store, collects suggestion values, and folds
// every failure into "no suggestions": completion must never fail loudly.
func completeFromStore(ctx context.Context, list func(context.Context, store.Store) ([]string, error)) []string {
	s, err := completionStore()
	if err != nil {
		return nil
	}
	defer func() { _ = s.Close() }()
	values, err := list(ctx, s)
	if err != nil {
		return nil
	}
	return values
}

func completeQueuedNumber(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 1 {
		return noFile(nil)
	}
	values := completeFromStore(cmd.Context(), func(ctx context.Context, s store.Store) ([]string, error) {
		rows, err := s.ListQueue(ctx, args[0])
		if err != nil {
			return nil, err
		}
		numbers := make([]string, 0, len(rows))
		for _, row := range rows {
			numbers = append(numbers, strconv.Itoa(row.Number))
		}
		return numbers, nil
	})
	return noFile(completePrefix(values, toComplete))
}

func completeAllowedAuthorRepo(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	values := append([]string{"*"}, config.Read().SortedRepos()...)
	values = append(values, completeFromStore(cmd.Context(), func(ctx context.Context, s store.Store) ([]string, error) {
		authors, err := s.ListAllowedAuthors(ctx, "")
		if err != nil {
			return nil, err
		}
		repos := make([]string, 0, len(authors))
		for _, author := range authors {
			repos = append(repos, author.Repo)
		}
		return repos, nil
	})...)
	return noFile(completePrefix(values, toComplete))
}

func completeAllowedAuthorHandle(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 1 {
		return noFile(nil)
	}
	values := completeFromStore(cmd.Context(), func(ctx context.Context, s store.Store) ([]string, error) {
		authors, err := s.ListAllowedAuthors(ctx, args[0])
		if err != nil {
			return nil, err
		}
		handles := make([]string, 0, len(authors))
		for _, author := range authors {
			handles = append(handles, author.GitHubHandle)
		}
		return handles, nil
	})
	return noFile(completePrefix(values, toComplete))
}
