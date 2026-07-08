package config

import (
	"sort"
	"strings"
)

// SortedRepos returns the watched repos as a presentation copy ordered by
// owner/repo. Discovery still consumes Config.Repos directly so config order
// remains behavior-neutral.
func (c Config) SortedRepos() []string {
	return sortRepos(c.Repos)
}

func sortRepos(repos []string) []string {
	sorted := append([]string(nil), repos...)
	sort.SliceStable(sorted, func(i, j int) bool {
		left := strings.ToLower(sorted[i])
		right := strings.ToLower(sorted[j])
		if left == right {
			return sorted[i] < sorted[j]
		}
		return left < right
	})
	return sorted
}
