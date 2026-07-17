// Package prref owns the small, shared owner/repo + PR number shape used by
// the CLI and dashboard surfaces.
package prref

import (
	"errors"
	"strconv"
	"strings"

	"github.com/shhac/agent-code-review/internal/config"
)

type Ref struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

func (r Ref) Valid() bool {
	return config.ValidRepoName(r.Repo) && r.Number > 0
}

func (r Ref) String() string {
	return r.Repo + "#" + strconv.Itoa(r.Number)
}

// Field-specific parse errors: callers name the offending field in their own
// envelope without re-deriving the checks.
var (
	ErrRepo   = errors.New("repo must be owner/name")
	ErrNumber = errors.New("PR number must be a positive integer")
)

// Parse validates the two textual halves of a PR reference. Repo is checked
// first, so with both fields invalid the repo error wins.
func Parse(repo, number string) (Ref, error) {
	if !config.ValidRepoName(repo) {
		return Ref{}, ErrRepo
	}
	n, err := strconv.Atoi(number)
	if err != nil || n <= 0 {
		return Ref{}, ErrNumber
	}
	return Ref{Repo: repo, Number: n}, nil
}

func ParseGitHubPull(raw string) (Ref, bool) {
	ref := strings.TrimSpace(raw)
	ref = strings.TrimPrefix(ref, "https://github.com/")
	parts := strings.Split(ref, "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return Ref{}, false
	}
	number, err := strconv.Atoi(parts[3])
	if err != nil {
		return Ref{}, false
	}
	parsed := Ref{Repo: parts[0] + "/" + parts[1], Number: number}
	return parsed, parsed.Valid()
}
