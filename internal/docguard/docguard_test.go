// A structural guard over doc comments, in the spirit of the UI's
// styles.test.ts: the thing it checks is not expressible in the language, and
// the bug it catches has already happened four times.
//
// Inserting a function between an existing doc comment and the function it
// documents fuses the two comment blocks, and godoc silently attributes the
// whole thing to the SECOND declaration while the first loses its
// documentation entirely. Nothing in gofmt, go vet or golangci-lint notices,
// and in a codebase where the comments carry most of the design rationale, a
// paragraph filed under the wrong name is worse than no paragraph.
//
// Known instances, all found by hand before this existed: prWhere wearing
// Enqueue's conflict contract, reviewInFlight wearing addToQueue's summary,
// retryAfterError wearing reviewRecord's, and usageRaw wearing costUSD's.
package docguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestDocCommentsNameTheirOwnDeclaration fires only when a comment's first
// word names a DIFFERENT declaration in the same package. That is deliberately
// narrower than Go's "comments should begin with the name" convention: prose
// that opens some other way is idiomatic enough here to be worth tolerating,
// while a comment opening with a real sibling's name is almost always this
// bug rather than a sentence that happens to start that way.
func TestDocCommentsNameTheirOwnDeclaration(t *testing.T) {
	root := ".."
	fset := token.NewFileSet()

	type decl struct {
		name, first, pos string
	}
	byPkg := map[string][]decl{}
	names := map[string]map[string]bool{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// The UI's node_modules is enormous and holds no Go.
			if d.Name() == "node_modules" || d.Name() == "assets" {
				return fs.SkipDir
			}
			return nil
		}
		// Test files are excluded: their comments explain scenarios rather
		// than declarations, so the sibling-name heuristic misfires on them.
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			return perr
		}
		pkg := f.Name.Name
		if names[pkg] == nil {
			names[pkg] = map[string]bool{}
		}
		for _, dcl := range f.Decls {
			name, doc := declName(dcl), declDoc(dcl)
			if name == "" {
				continue
			}
			names[pkg][name] = true
			if doc == nil {
				continue
			}
			if first := firstWord(doc.Text()); first != "" && first != name {
				byPkg[pkg] = append(byPkg[pkg], decl{name, first, fset.Position(dcl.Pos()).String()})
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for pkg, decls := range byPkg {
		for _, d := range decls {
			if names[pkg][d.first] {
				t.Errorf("%s: doc comment on %s begins with %q, which is a different declaration "+
					"in this package — the two comment blocks are probably fused, and %s has lost its docs",
					d.pos, d.name, d.first, d.first)
			}
		}
	}
}

func declName(d ast.Decl) string {
	switch v := d.(type) {
	case *ast.FuncDecl:
		return v.Name.Name
	case *ast.GenDecl:
		// Only single-spec blocks: a grouped var/const block's doc describes
		// the group, not any one member.
		if len(v.Specs) != 1 {
			return ""
		}
		switch s := v.Specs[0].(type) {
		case *ast.TypeSpec:
			return s.Name.Name
		case *ast.ValueSpec:
			if len(s.Names) == 1 {
				return s.Names[0].Name
			}
		}
	}
	return ""
}

func declDoc(d ast.Decl) *ast.CommentGroup {
	switch v := d.(type) {
	case *ast.FuncDecl:
		return v.Doc
	case *ast.GenDecl:
		return v.Doc
	}
	return nil
}

func firstWord(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.IndexAny(text, " \t\n"); i > 0 {
		return text[:i]
	}
	return text
}
