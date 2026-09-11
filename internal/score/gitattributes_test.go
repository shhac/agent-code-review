package score

import "testing"

func TestIsExcludedRootPatterns(t *testing.T) {
	a := ParseAttrs(map[string]string{
		"": `
# Comment, ignored
package-lock.json  linguist-generated=true
*.pb.go            linguist-generated
vendor/            linguist-vendored
dist/**            linguist-generated=true
/only-at-root.txt  linguist-generated
docs/*.md          linguist-generated
`,
	})

	excluded := []string{
		"package-lock.json",
		"nested/deep/package-lock.json", // no slash in pattern: basename, any depth
		"api/v1/types.pb.go",
		"vendor/github.com/x/y.go",
		"dist/app.js",
		"dist/assets/deep/bundle.css",
		"only-at-root.txt",
		"docs/readme.md",
	}
	for _, p := range excluded {
		if !a.IsExcluded(p) {
			t.Errorf("IsExcluded(%q) = false, want true", p)
		}
	}

	kept := []string{
		"main.go",
		"api/v1/types.go",
		"vendored/thing.go",    // not the vendor/ directory
		"sub/only-at-root.txt", // leading slash anchors it
		"docs/guide/deep.md",   // * does not cross a separator
		"distribution/app.js",
	}
	for _, p := range kept {
		if a.IsExcluded(p) {
			t.Errorf("IsExcluded(%q) = true, want false", p)
		}
	}
}

// Last matching line wins, which is how a repo carves an exception out of a
// broad rule.
func TestLastMatchWins(t *testing.T) {
	a := ParseAttrs(map[string]string{
		"": `
*.json          linguist-generated
tsconfig.json   -linguist-generated
`,
	})
	if !a.IsExcluded("package-lock.json") {
		t.Error("package-lock.json should be excluded by the broad rule")
	}
	if a.IsExcluded("tsconfig.json") {
		t.Error("tsconfig.json should be rescued by the later -linguist-generated")
	}
}

func TestFalseFormsAreNotExcluded(t *testing.T) {
	a := ParseAttrs(map[string]string{
		"": `
a.json  linguist-generated=false
b.json  -linguist-generated
c.json  linguist-generated=0
d.json  linguist-generated=true
`,
	})
	for _, p := range []string{"a.json", "b.json", "c.json"} {
		if a.IsExcluded(p) {
			t.Errorf("IsExcluded(%q) = true, want false", p)
		}
	}
	if !a.IsExcluded("d.json") {
		t.Error("d.json should be excluded")
	}
}

// A deeper .gitattributes overrides a shallower one, whatever order the files
// arrive in from the fetch.
func TestDeeperFileOverridesShallower(t *testing.T) {
	a := ParseAttrs(map[string]string{
		"":               "*.go  linguist-generated\n",
		"internal/score": "*.go  -linguist-generated\n",
	})
	if !a.IsExcluded("main.go") {
		t.Error("root rule should still apply outside the deeper directory")
	}
	if a.IsExcluded("internal/score/score.go") {
		t.Error("deeper .gitattributes should override the root rule")
	}
}

// A nested file governs only its own subtree.
func TestNestedFileScopedToItsDirectory(t *testing.T) {
	a := ParseAttrs(map[string]string{
		"internal/dashboard/assets": "**  linguist-generated\n",
	})
	if !a.IsExcluded("internal/dashboard/assets/index-abc123.js") {
		t.Error("the assets bundle should be excluded")
	}
	if a.IsExcluded("internal/dashboard/dashboard.go") {
		t.Error("a sibling outside the directory must not be excluded")
	}
}

func TestDoubleStarCrossesSeparatorsAndMatchesZeroDirs(t *testing.T) {
	a := ParseAttrs(map[string]string{
		"": "src/**/generated.ts  linguist-generated\n",
	})
	for _, p := range []string{"src/generated.ts", "src/a/generated.ts", "src/a/b/c/generated.ts"} {
		if !a.IsExcluded(p) {
			t.Errorf("IsExcluded(%q) = false, want true", p)
		}
	}
	if a.IsExcluded("other/a/generated.ts") {
		t.Error("the src/ anchor must still hold")
	}
}

func TestCharacterClasses(t *testing.T) {
	a := ParseAttrs(map[string]string{
		"": `
file[0-9].txt   linguist-generated
skip[!0-9].txt  linguist-generated
`,
	})
	if !a.IsExcluded("file3.txt") {
		t.Error("file3.txt should match [0-9]")
	}
	if a.IsExcluded("filex.txt") {
		t.Error("filex.txt should not match [0-9]")
	}
	if !a.IsExcluded("skipx.txt") {
		t.Error("skipx.txt should match the negated class")
	}
	if a.IsExcluded("skip7.txt") {
		t.Error("skip7.txt should not match the negated class")
	}
}

func TestQuotedPatternWithSpaces(t *testing.T) {
	a := ParseAttrs(map[string]string{
		"": "\"my generated file.txt\"  linguist-generated\n",
	})
	if !a.IsExcluded("my generated file.txt") {
		t.Error("quoted pattern with spaces should match")
	}
}

// Scoring is an enrichment. A malformed attributes file must degrade to
// "nothing excluded", never to a failed review.
func TestMalformedLinesAreSkipped(t *testing.T) {
	a := ParseAttrs(map[string]string{
		"": `
[attr]binary  -diff -text
pattern-with-no-attributes
[unclosed  linguist-generated
"unterminated quote  linguist-generated
good.json  linguist-generated
`,
	})
	if !a.IsExcluded("good.json") {
		t.Error("the one valid line should still apply")
	}
	if a.IsExcluded("pattern-with-no-attributes") {
		t.Error("a line with no attributes says nothing")
	}
}

// Lines about other attributes entirely are not our business.
func TestUnrelatedAttributesIgnored(t *testing.T) {
	a := ParseAttrs(map[string]string{
		"": `
*.png   binary
*.sh    text eol=lf
*.lock  linguist-generated
`,
	})
	if a.IsExcluded("logo.png") || a.IsExcluded("run.sh") {
		t.Error("unrelated attributes must not exclude anything")
	}
	if !a.IsExcluded("Cargo.lock") {
		t.Error("*.lock should be excluded")
	}
}

func TestCRLFLineEndings(t *testing.T) {
	a := ParseAttrs(map[string]string{"": "*.min.js  linguist-generated\r\nmain.go  -linguist-generated\r\n"})
	if !a.IsExcluded("app.min.js") {
		t.Error("CRLF line endings should parse")
	}
}

// A repo that declares nothing, and one whose declarations are all comments,
// both exclude nothing rather than erroring or excluding everything.
func TestNoDeclarationsExcludeNothing(t *testing.T) {
	for _, a := range []Attrs{
		ParseAttrs(nil),
		ParseAttrs(map[string]string{}),
		ParseAttrs(map[string]string{"": "# just a comment\n"}),
	} {
		if a.IsExcluded("anything.json") || a.IsExcluded("vendor/x.go") {
			t.Error("a repo declaring nothing must exclude nothing")
		}
	}
	if !ParseAttrs(map[string]string{"": "x  linguist-generated\n"}).IsExcluded("x") {
		t.Error("a real rule should still apply")
	}
}

func TestVendoredAndGeneratedBothExclude(t *testing.T) {
	a := ParseAttrs(map[string]string{"": "v/**  linguist-vendored\ng/**  linguist-generated\n"})
	if !a.IsExcluded("v/x.go") || !a.IsExcluded("g/y.go") {
		t.Error("both attributes should exclude")
	}
}

// git's `!attr` makes the attribute UNSPECIFIED, which for these two means the
// file is not generated and not vendored. Treating it as "leave the earlier
// value alone" meant a repo could not carve an exception out of a broad rule
// the way git says it can.
func TestBangAttrUndoesAnEarlierRule(t *testing.T) {
	a := ParseAttrs(map[string]string{"": `
*.json          linguist-generated
keep.json       !linguist-generated
vendor/**       linguist-vendored
vendor/ours.go  !linguist-vendored
`})
	if !a.IsExcluded("package-lock.json") {
		t.Error("the broad rule should still apply")
	}
	if a.IsExcluded("keep.json") {
		t.Error("!linguist-generated must undo the earlier rule, as git does")
	}
	if a.IsExcluded("vendor/ours.go") {
		t.Error("!linguist-vendored must undo the earlier rule")
	}
	if !a.IsExcluded("vendor/dep.go") {
		t.Error("the rest of vendor/ should stay excluded")
	}
}

// A POSIX bracket expression contains a ] that closes [: :] rather than the
// class, so stopping at the first one produced an invalid regexp and the rule
// was dropped with no error anywhere: files the repo had declared generated
// were silently counted as churn.
func TestPosixBracketExpressions(t *testing.T) {
	a := ParseAttrs(map[string]string{"": `
file[[:digit:]].go       linguist-generated
gen[[:alpha:]][[:digit:]].ts  linguist-generated
`})
	for _, p := range []string{"file1.go", "file9.go"} {
		if !a.IsExcluded(p) {
			t.Errorf("IsExcluded(%q) = false; the POSIX class should have compiled", p)
		}
	}
	if a.IsExcluded("filex.go") {
		t.Error("[[:digit:]] must not match a letter")
	}
	if !a.IsExcluded("gena1.ts") {
		t.Error("two POSIX classes in one pattern should compile")
	}

	// And the pattern must actually compile rather than being skipped.
	if _, err := compilePattern("file[[:digit:]].go"); err != nil {
		t.Errorf("compilePattern: %v", err)
	}
}

// An unterminated POSIX class must still degrade to "rule skipped" rather than
// panicking or matching everything.
func TestMalformedPosixClassIsSkipped(t *testing.T) {
	a := ParseAttrs(map[string]string{"": "bad[[:digit.go  linguist-generated\ngood.go  linguist-generated\n"})
	if !a.IsExcluded("good.go") {
		t.Error("the valid rule should still apply")
	}
}
