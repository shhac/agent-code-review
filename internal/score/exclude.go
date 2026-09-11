package score

import "regexp"

// FileStat is one changed file's line counts, as GitHub reports them.
type FileStat struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
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
	if e.Attrs.IsExcluded(path) {
		return true
	}
	for _, re := range e.globs {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}

// Apply sums the files that count. The excluded ones are counted but not
// summed, so the history row can say "3 files were left out" rather than
// silently reporting a smaller diff than GitHub does.
func (e Exclusions) Apply(files []FileStat) Totals {
	var t Totals
	for _, f := range files {
		if e.Excludes(f.Path) {
			t.ExcludedFiles++
			continue
		}
		t.Additions += f.Additions
		t.Deletions += f.Deletions
	}
	return t
}
