package review

import (
	"errors"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
)

// Preview validation sentinels. The two preview surfaces (CLI and dashboard)
// keep their own error wording and envelopes; these let them map the shared
// validation to it without re-implementing the checks.
var (
	ErrBadCandidateType = errors.New("invalid candidate type")
	ErrBadRepo          = errors.New("invalid repo")
)

// SampleRepo is the placeholder repo for synthetic prompt previews. The CLI's
// `prompts preview` and the dashboard both render this fixture, so it lives in
// one place to keep them identical.
const SampleRepo = "example-org/example-repo"

// SampleCandidate builds the synthetic candidate used for prompt previews from
// an already-defaulted, already-validated repo and candidate type. Both preview
// paths assemble from this one fixture so they can't drift.
func SampleCandidate(repo, candidateType string) store.Candidate {
	return store.Candidate{
		Repo:    repo,
		Number:  123,
		Type:    candidateType,
		Author:  "example-author",
		URL:     "https://github.com/" + repo + "/pull/123",
		HeadSHA: "0000000000000000000000000000000000000000",
	}
}

// PreviewResult is the assembled preview for one shaped synthetic candidate:
// the candidate echo both surfaces render, the assembled prompt, and the
// per-rule trace.
type PreviewResult struct {
	Repo          string
	CandidateType string
	Facts         Facts
	Prompt        string
	Rules         []RuleTrace
}

// Preview assembles the synthetic-candidate prompt preview shared by the
// CLI's `prompts preview` and the dashboard's preview endpoint: defaulting,
// validation, the sample fixture, assembly, and rule tracing live here in
// one place so the two surfaces cannot drift (SampleRepo/SampleCandidate
// started this extraction; this finishes it). repo and candidateType may be
// empty and are defaulted; validation failures return the sentinel errors
// above for the caller to wrap in its own envelope.
func Preview(cfg config.Config, repo, candidateType string, f Facts) (PreviewResult, error) {
	if candidateType == "" {
		candidateType = store.TypeNew
	}
	if !config.ValidCandidateType(candidateType) {
		return PreviewResult{}, ErrBadCandidateType
	}
	if repo == "" {
		repo = SampleRepo
	}
	if !config.ValidRepoName(repo) {
		return PreviewResult{}, ErrBadRepo
	}
	sample := SampleCandidate(repo, candidateType)
	return PreviewResult{
		Repo:          sample.Repo,
		CandidateType: sample.Type,
		Facts:         f,
		Prompt:        BuildPrompt(cfg, sample, f),
		Rules:         ExplainRules(cfg, sample, f),
	}, nil
}
