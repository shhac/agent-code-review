package discover

import (
	"reflect"
	"testing"

	"github.com/shhac/agent-code-review/internal/score"
)

func files(paths ...string) []score.FileStat {
	out := make([]score.FileStat, 0, len(paths))
	for _, p := range paths {
		out = append(out, score.FileStat{Path: p})
	}
	return out
}

// attrDirs decides which .gitattributes get fetched, which decides which files
// count as generated, which decides every scored PR's size. It is pure, so
// there is no excuse for it to be unpinned.
func TestAttrDirs(t *testing.T) {
	cases := []struct {
		name  string
		files []score.FileStat
		want  []string
	}{
		{
			name: "no files still asks for the root",
			want: nil,
		},
		{
			name:  "root-level files need only the root",
			files: files("main.go", "README.md"),
			want:  []string{""},
		},
		{
			name:  "each file's own directory, deduped and sorted",
			files: files("a/x.go", "a/y.go", "b/z.go"),
			want:  []string{"", "a", "b"},
		},
		{
			name:  "a leading slash is not a directory",
			files: files("/main.go"),
			want:  []string{""},
		},
		{
			name:  "leading slashes are stripped before splitting",
			files: files("/a/b/x.go"),
			want:  []string{"", "a", "a/b"},
		},
		{
			// git applies a/.gitattributes to a/b/c/deep.go, so every ancestor
			// has to be asked for. Skipping them let a repo that marked its
			// generated files one level down have them counted as churn.
			name:  "every ancestor is walked, because git applies them all",
			files: files("a/b/c/deep.go"),
			want:  []string{"", "a", "a/b", "a/b/c"},
		},
		{
			name:  "ancestor chains of several files are unioned, not repeated",
			files: files("a/b/one.go", "a/b/two.go", "a/c/three.go"),
			want:  []string{"", "a", "a/b", "a/c"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := attrDirs(tc.files)
			if tc.want == nil {
				if len(got) != 0 {
					t.Errorf("attrDirs() = %v, want none", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("attrDirs() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The alias query is built per candidate directory; a malformed document would
// fail every fetch, so its shape is worth pinning.
func TestAttrsQueryShape(t *testing.T) {
	q := attrsQuery(2)
	for _, want := range []string{
		"$owner:String!", "$repo:String!", "$e0:String!", "$e1:String!",
		"a0: object(expression:$e0)", "a1: object(expression:$e1)", "... on Blob { text }",
	} {
		if !contains(q, want) {
			t.Errorf("attrsQuery(2) is missing %q:\n%s", want, q)
		}
	}
	// Paths reach the server as variables; interpolating them into the
	// document is how an injection or a quoting bug would get in.
	if contains(q, ".gitattributes") {
		t.Error("attrsQuery must not interpolate paths into the document")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func TestSplitRepo(t *testing.T) {
	owner, name, err := splitRepo("owner/name")
	if err != nil || owner != "owner" || name != "name" {
		t.Errorf("splitRepo(owner/name) = %q/%q err=%v", owner, name, err)
	}
	for _, bad := range []string{"", "owner", "/name", "owner/", "owner"} {
		if _, _, err := splitRepo(bad); err == nil {
			t.Errorf("splitRepo(%q) should error", bad)
		}
	}
}

// A gh error should name the call, not reproduce the whole GraphQL document:
// the static query buries the one line that says what actually went wrong.
func TestElideQueryKeepsTheUsefulArgv(t *testing.T) {
	got := elideQuery([]string{"api", "graphql", "-f", "owner=cli", "-F", "number=1", "-f", "query=query($x:String!){\n  repository\n}"})
	joined := ""
	for _, a := range got {
		joined += a + " "
	}
	for _, want := range []string{"owner=cli", "number=1", "query=<graphql>"} {
		if !contains(joined, want) {
			t.Errorf("argv = %q, want it to contain %q", joined, want)
		}
	}
	if contains(joined, "repository") {
		t.Errorf("argv = %q, want the document elided", joined)
	}
}
