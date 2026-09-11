package score

import (
	"regexp"
	"sort"
	"strings"
)

// Generated- and vendored-file detection, read from the REPO'S OWN
// .gitattributes rather than from any list we keep.
//
// A lockfile or a committed build bundle is thousands of lines nobody wrote,
// and counting it as churn buries a real change in the largest size bucket.
// GitHub already has a convention for saying so, Linguist's
// `linguist-generated` / `linguist-vendored`, and it is the repo's to declare:
// we do not know which repos this runs against, and a list we owned would be
// wrong for all of them. It is also the same declaration that collapses those
// files in GitHub's own diff view, so a repo that fixes its scores fixes its
// review experience with one line.
//
// What this deliberately does NOT do is reimplement Linguist's BUILT-IN
// heuristics, which recognise package-lock.json with no .gitattributes at all.
// That is a Ruby library and a pattern list, and the pattern list is the part
// we are declining to own. A repo that has not marked its generated files gets
// inflated churn until it does; config's exclude_paths is the operator's
// escape hatch in the meantime.

const (
	attrGenerated = "linguist-generated"
	attrVendored  = "linguist-vendored"
)

// rule is one line of a .gitattributes file: a compiled pattern and what it
// says about the two attributes we read. nil means the line is silent about
// that attribute, which is different from saying false.
type rule struct {
	re        *regexp.Regexp
	generated *bool
	vendored  *bool
}

// attrFile is one .gitattributes, remembered with the directory it governs.
// Its rules apply only to paths beneath that directory.
type attrFile struct {
	dir   string // "" for the repo root, else "a/b" with no trailing slash
	rules []rule
}

// Attrs is the parsed .gitattributes set for one commit.
type Attrs struct {
	files []attrFile
}

// ParseAttrs builds the matcher from raw .gitattributes contents, keyed by the
// directory each was found in ("" for the repo root). Unparseable lines are
// skipped rather than failing: scoring is an enrichment and a malformed
// attributes file must never cost a review.
func ParseAttrs(byDir map[string]string) Attrs {
	var a Attrs
	for dir, text := range byDir {
		f := attrFile{dir: strings.Trim(dir, "/")}
		for _, line := range strings.Split(text, "\n") {
			if r, ok := parseLine(strings.TrimRight(line, "\r")); ok {
				f.rules = append(f.rules, r)
			}
		}
		if len(f.rules) > 0 {
			a.files = append(a.files, f)
		}
	}
	// Shallow before deep: git lets a deeper .gitattributes override a
	// shallower one, and applying them in this order makes the last write win
	// without any explicit precedence bookkeeping.
	sort.Slice(a.files, func(i, j int) bool {
		di, dj := depth(a.files[i].dir), depth(a.files[j].dir)
		if di != dj {
			return di < dj
		}
		return a.files[i].dir < a.files[j].dir
	})
	return a
}

// IsExcluded reports whether path is marked generated or vendored.
//
// Precedence is git's: a deeper .gitattributes beats a shallower one, and
// within one file the LAST matching line wins. Both fall out of evaluating
// every rule in order and keeping the final state.
func (a Attrs) IsExcluded(path string) bool {
	path = strings.TrimPrefix(path, "/")
	var generated, vendored bool
	for _, f := range a.files {
		rel, ok := under(f.dir, path)
		if !ok {
			continue
		}
		for _, r := range f.rules {
			if !r.re.MatchString(rel) {
				continue
			}
			if r.generated != nil {
				generated = *r.generated
			}
			if r.vendored != nil {
				vendored = *r.vendored
			}
		}
	}
	return generated || vendored
}

// under returns path relative to dir, and whether it is beneath it at all.
func under(dir, path string) (string, bool) {
	if dir == "" {
		return path, true
	}
	prefix := dir + "/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	return strings.TrimPrefix(path, prefix), true
}

func depth(dir string) int {
	if dir == "" {
		return 0
	}
	return strings.Count(dir, "/") + 1
}

// parseLine reads one `<pattern> <attr>...` line. Reports false for blanks,
// comments, macro definitions ([attr]foo, which we do not expand), lines whose
// pattern will not compile, and lines that say nothing about either attribute.
func parseLine(line string) (rule, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[attr]") {
		return rule{}, false
	}
	pattern, rest, ok := splitPattern(line)
	if !ok {
		return rule{}, false
	}
	r := rule{}
	for _, tok := range strings.Fields(rest) {
		name, val := parseAttrToken(tok)
		switch name {
		case attrGenerated:
			r.generated = val
		case attrVendored:
			r.vendored = val
		}
	}
	if r.generated == nil && r.vendored == nil {
		return rule{}, false
	}
	re, err := compilePattern(pattern)
	if err != nil {
		return rule{}, false
	}
	r.re = re
	return r, true
}

// splitPattern peels the pattern off the front of a line, honouring the
// quoted form git writes for patterns containing spaces.
func splitPattern(line string) (pattern, rest string, ok bool) {
	if strings.HasPrefix(line, `"`) {
		end := strings.Index(line[1:], `"`)
		if end < 0 {
			return "", "", false
		}
		return line[1 : 1+end], strings.TrimSpace(line[2+end:]), true
	}
	i := strings.IndexAny(line, " \t")
	if i < 0 {
		return "", "", false // a pattern with no attributes says nothing
	}
	return line[:i], strings.TrimSpace(line[i:]), true
}

// parseAttrToken reads one attribute token into a name and a value. nil means
// the token says nothing about that attribute, so an earlier rule stands.
//
// git's `!attr` makes the attribute UNSPECIFIED, and for the two attributes we
// read, unspecified means the file is not generated and not vendored. Treating
// it as "leave the earlier value alone" (which it was) meant a repo could not
// carve an exception out of a broad rule the way git says it can: given
// `*.json linguist-generated` then `keep.json !linguist-generated`, keep.json
// stayed excluded. Unsetting (`-attr`) and unspecifying (`!attr`) differ only
// in inheritance semantics this matcher has no need to model, so both are
// false here.
func parseAttrToken(tok string) (string, *bool) {
	yes, no := true, false
	switch {
	case strings.HasPrefix(tok, "-"):
		return strings.TrimPrefix(tok, "-"), &no
	case strings.HasPrefix(tok, "!"):
		return strings.TrimPrefix(tok, "!"), &no
	}
	name, val, found := strings.Cut(tok, "=")
	if !found {
		return name, &yes
	}
	switch strings.ToLower(val) {
	case "false", "0", "no", "unset":
		return name, &no
	}
	return name, &yes
}

// compilePattern translates a gitignore-style pattern into a regexp matched
// against a path relative to the .gitattributes' own directory.
//
// A pattern containing a slash anywhere but the end is ANCHORED to that
// directory; one without is matched against the basename at any depth. `*`
// stops at a separator and `**` crosses them.
func compilePattern(pat string) (*regexp.Regexp, error) {
	anchored := strings.Contains(strings.TrimSuffix(pat, "/"), "/")
	pat = strings.TrimPrefix(pat, "/")
	// git says a trailing slash matches nothing (attributes do not apply to
	// directories), but it is written by people who mean "everything in here",
	// and reading it that way is the useful answer for vendor/ and dist/.
	if strings.HasSuffix(pat, "/") {
		pat += "**"
	}

	var b strings.Builder
	b.WriteString("^")
	if !anchored {
		b.WriteString("(?:.*/)?")
	}

	for i := 0; i < len(pat); i++ {
		switch c := pat[i]; c {
		case '*':
			if i+1 < len(pat) && pat[i+1] == '*' {
				i++
				// A `**/` segment matches zero or more directories, so that
				// `a/**/b` also matches `a/b`.
				if i+1 < len(pat) && pat[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
					continue
				}
				b.WriteString(".*")
				continue
			}
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		case '[':
			end := closingBracket(pat, i)
			if end < 0 {
				b.WriteString(regexp.QuoteMeta(string(c)))
				continue
			}
			b.WriteString(translateClass(pat[i : end+1]))
			i = end
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// closingBracket finds the ] ending a character class, honouring the leading
// ! or ^ and a ] in first position, both of which are literal there, and
// skipping over POSIX bracket expressions.
//
// The POSIX skip is not decorative. `[[:digit:]]` contains a ] that closes the
// inner [: :] rather than the class, so stopping at the first one yielded
// `[[:digit:]` -> an invalid regexp -> a rule dropped with no error anywhere,
// silently counting files the repo had declared generated. Go's regexp
// understands these classes natively once the class survives intact.
func closingBracket(pat string, open int) int {
	i := open + 1
	if i < len(pat) && (pat[i] == '!' || pat[i] == '^') {
		i++
	}
	if i < len(pat) && pat[i] == ']' {
		i++
	}
	for ; i < len(pat); i++ {
		if pat[i] == '[' && i+1 < len(pat) && pat[i+1] == ':' {
			if end := strings.Index(pat[i+2:], ":]"); end >= 0 {
				i += 2 + end + 1 // land on the ] of :], the loop's i++ moves past
				continue
			}
		}
		if pat[i] == ']' {
			return i
		}
	}
	return -1
}

// translateClass converts a glob character class to a regexp one. The only
// difference is the negation marker.
func translateClass(class string) string {
	body := class[1 : len(class)-1]
	if strings.HasPrefix(body, "!") {
		body = "^" + body[1:]
	}
	return "[" + body + "]"
}
