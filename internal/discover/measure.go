package discover

// Measuring a PR's size: the one pipeline that turns a pull request into the
// line counts a score is computed from.
//
// It lives here, in the package that talks to gh, rather than in the scheduler
// that first needed it, because a second caller arrived. `score refetch` has to
// measure a PR exactly as completion did, and a second copy of "fetch the
// files, read the repo's declarations, apply the exclusions" is precisely the
// shape that already produced one silent divergence in this feature.

import (
	"context"

	"github.com/shhac/agent-code-review/internal/score"
	"github.com/shhac/agent-code-review/internal/store"
)

// DiffFn fetches a PR's per-file line counts. A seam so callers' tests never
// shell out to gh.
type DiffFn func(ctx context.Context, repo string, number int) (PRDiff, error)

// AttrsFn fetches the repo's .gitattributes declarations covering these files.
type AttrsFn func(ctx context.Context, repo, ref string, files []score.FileStat) (map[string]string, error)

// Measurement is one PR's measured size, plus the per-file detail that lets an
// exclusion policy be re-applied later without asking GitHub again.
type Measurement struct {
	Stats store.DiffStats
	// Files carries each file's counts and the repo's verdict on it. Empty
	// when the listing was truncated, since a partial list cannot support
	// exclusion and must not be stored as though it were complete.
	Files []score.FileStat
	// Notes are non-fatal degradations worth surfacing: a truncated listing,
	// unreadable declarations. The measurement still stands.
	Notes []string
}

// Measurer holds the two fetch seams. The zero value uses the real ones.
type Measurer struct {
	Diff  DiffFn
	Attrs AttrsFn
}

func (m Measurer) diff() DiffFn {
	if m.Diff != nil {
		return m.Diff
	}
	return PRFiles
}

func (m Measurer) attrs() AttrsFn {
	if m.Attrs != nil {
		return m.Attrs
	}
	return GitAttributes
}

// Measure reads a PR's size and works out how much of it counts.
//
// It takes the RESOLVED ruleset rather than the config, so that the policy
// deciding which lines count is the same one whose hash gets recorded beside
// the score. Reading the raw settings here was a way for the two to disagree:
// ResolveScoring falls back to the shipped defaults when a ruleset is invalid,
// while the raw accessors do not, so an unusable scoring block could exclude
// every file under its own exclude_paths and then record the DEFAULT hash over
// the result. That row looks current to `--stale` forever, and nothing can
// tell it was measured under a policy nobody chose.
//
// An error means the size is unknown, which callers must record as unknown
// rather than as zero: a PR nobody measured and a PR with nothing in it are
// different facts, and only one of them is worth no points.
func (m Measurer) Measure(ctx context.Context, rules score.Rules, repo string, number int) (Measurement, error) {
	diff, err := m.diff()(ctx, repo, number)
	if err != nil {
		return Measurement{}, err
	}

	out := Measurement{Stats: store.DiffStats{
		Additions:    diff.Additions,
		Deletions:    diff.Deletions,
		ChangedFiles: diff.ChangedFiles,
		DiffSHA:      diff.HeadSHA,
	}}

	// A truncated file list cannot support exclusion: we would be scoring the
	// first 3000 files and silently calling the rest zero. Fall back to the
	// raw totals, which are complete whatever the listing did, and store no
	// per-file detail, because a partial list would let a later recount claim
	// a precision it never had.
	if diff.Truncated {
		out.Notes = append(out.Notes, "GitHub truncated the file list; scoring the raw totals with no exclusions")
		out.Stats.ScoredAdditions, out.Stats.ScoredDeletions = diff.Additions, diff.Deletions
		return out, nil
	}

	var attrs score.Attrs
	if rules.UseGitattributes {
		byDir, err := m.attrs()(ctx, repo, "HEAD", diff.Files)
		if err != nil {
			// The repo's declarations are an enrichment on an enrichment. Not
			// reading them means counting generated lines, which is a worse
			// score rather than no score.
			out.Notes = append(out.Notes, "could not read .gitattributes, counting every file: "+err.Error())
		} else {
			attrs = score.ParseAttrs(byDir)
		}
	}

	// Apply marks each file with the repo's verdict as it totals them, so the
	// detail stored below is the resolved fact rather than a promise to
	// re-derive it.
	totals := score.NewExclusions(attrs, rules.ExcludePaths).Apply(diff.Files)
	out.Stats.ScoredAdditions = totals.Additions
	out.Stats.ScoredDeletions = totals.Deletions
	out.Stats.ExcludedFiles = totals.ExcludedFiles
	out.Files = diff.Files
	return out, nil
}
