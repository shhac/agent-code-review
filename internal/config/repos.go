package config

import (
	"sort"
	"strings"
)

// SortedRepos returns a presentation copy of repos ordered by owner/repo.
func SortedRepos(repos []string) []string {
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
