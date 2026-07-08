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

func ParseArgs(args []string) (Ref, error) {
	number, err := strconv.Atoi(args[1])
	if err != nil {
		return Ref{}, err
	}
	ref := Ref{Repo: args[0], Number: number}
	if !ref.Valid() {
		return Ref{}, errors.New("invalid PR reference")
	}
	return ref, nil
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
