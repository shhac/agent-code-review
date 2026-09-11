package score

import "regexp"

// FileStat is one changed file's line counts, as GitHub reports them, plus
// what the repo said about it.
//
// Generated is not fetched: it is RESOLVED from the repo's .gitattributes at
// measurement time and then travels with the file. Storing the resolved fact
// rather than the declarations that produced it is what lets an exclusion
// policy be re-applied later with no network at all: the operator's globs are
// ours to re-run offline, and whether the REPO called a file generated is the
// one input we could not otherwise recover without re-reading its
// .gitattributes at the revision we reviewed.
type FileStat struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Generated bool   `json:"generated,omitempty"`
}

// Totals is a PR's line counts after exclusion, with enough detail for a
// surprising score to explain itself.
type Totals struct {
	Additions     int `json:"additions"`
	Deletions     int `json:"deletions"`
	ExcludedFiles int `json:"excluded_files"`
}

// Exclusions decides which files do not count toward churn: the repo's own
// .gitattributes, plus whatever globs the operator added in config for repos
// that have not adopted the convention.
type Exclusions struct {
	Attrs Attrs
	globs []*regexp.Regexp
}

// NewExclusions compiles the operator's globs alongside the repo's attributes.
// A glob that will not compile is dropped rather than returned as an error:
// one bad line in config must not stop every review from being scored.
func NewExclusions(a Attrs, globs []string) Exclusions {
	e := Exclusions{Attrs: a}
	for _, g := range globs {
		if re, err := compilePattern(g); err == nil {
			e.globs = append(e.globs, re)
		}
	}
	return e
}

// Excludes reports whether path is left out of the scored line counts.
func (e Exclusions) Excludes(path string) bool {
	return e.Attrs.IsExcluded(path) || e.matchesGlob(path)
}

// matchesGlob reports whether any of the operator's own globs covers path.
func (e Exclusions) matchesGlob(path string) bool {
	for _, re := range e.globs {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}

// Apply sums the files that count, and records on each one whether the REPO's
// own declarations covered it. The excluded files are counted but not summed,
// so the history row can say "3 files were left out" rather than silently
// reporting a smaller diff than GitHub does.
//
// It writes Generated back into files so the caller can persist the resolved
// fact. Marking here rather than in a separate pass keeps the two from
// disagreeing about what the declarations said.
func (e Exclusions) Apply(files []FileStat) Totals {
	var t Totals
	for i := range files {
		files[i].Generated = e.Attrs.IsExcluded(files[i].Path)
		if files[i].Generated || e.matchesGlob(files[i].Path) {
			t.ExcludedFiles++
			continue
		}
		t.Additions += files[i].Additions
		t.Deletions += files[i].Deletions
	}
	return t
}

// Recount re-applies an exclusion policy to files measured earlier, with no
// network access.
//
// This is what makes a change to exclude_paths or use_gitattributes a
// recompute rather than a re-fetch: the operator's globs are ours to re-run,
// and the repo's verdict on each file was resolved at measurement time and
// stored. A change to the REPO's own .gitattributes is deliberately NOT
// covered, because that is the repo changing its mind rather than us changing
// our policy, and honouring it means re-reading the declarations at the
// revision we reviewed.
func Recount(files []FileStat, r Rules) Totals {
	e := NewExclusions(Attrs{}, r.ExcludePaths)
	var t Totals
	for _, f := range files {
		if (r.UseGitattributes && f.Generated) || e.matchesGlob(f.Path) {
			t.ExcludedFiles++
			continue
		}
		t.Additions += f.Additions
		t.Deletions += f.Deletions
	}
	return t
}
