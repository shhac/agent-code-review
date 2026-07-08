package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/shhac/agent-code-review/internal/store"
)

// fakeQueueStore stubs ListQueue; everything else panics via the embedded nil
// interface, so an unexpected store dependency shows up loudly.
type fakeQueueStore struct {
	store.Store
	queue []store.Candidate
}

func (f *fakeQueueStore) ListQueue(context.Context, string) ([]store.Candidate, error) {
	return f.queue, nil
}

// TestFindQueued pins the skip command's lookup: present rows come back with
// their metadata (the head SHA feeds the SKIPPED history row); absent rows
// report found=false, which the command turns into an error instead of a
// dangling outcome.
func TestFindQueued(t *testing.T) {
	s := &fakeQueueStore{queue: []store.Candidate{
		{Repo: "o/r", Number: 1, HeadSHA: "sha1"},
		{Repo: "o/r", Number: 2, HeadSHA: "sha2"},
	}}
	c, found, err := findQueued(context.Background(), s, "o/r", 2)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v, want present", found, err)
	}
	if c.HeadSHA != "sha2" {
		t.Errorf("wrong row: %+v", c)
	}
	if _, found, _ := findQueued(context.Background(), s, "o/r", 3); found {
		t.Error("absent PR must report found=false")
	}
}

func TestParseRepoNumber(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		repo    string
		number  int
		wantErr bool
	}{
		{"valid", []string{"o/r", "7"}, "o/r", 7, false},
		{"bad repo", []string{"not-a-repo", "7"}, "", 0, true},
		{"non integer", []string{"o/r", "wat"}, "", 0, true},
		{"zero", []string{"o/r", "0"}, "", 0, true},
		{"negative", []string{"o/r", "-1"}, "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, number, err := parseRepoNumber(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil || repo != tc.repo || number != tc.number {
				t.Fatalf("parseRepoNumber = %q, %d, %v; want %q, %d, nil", repo, number, err, tc.repo, tc.number)
			}
		})
	}
}

// TestStreamFile covers `queue log`'s read path for finished reviews: the
// whole file is copied to out, and a missing log surfaces an error instead
// of an empty stream. The follow loop is deliberately untested (timing).
func TestStreamFile(t *testing.T) {
	t.Run("copies the whole file", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "agent.log")
		if err := os.WriteFile(p, []byte("line1\nline2\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := streamFile(context.Background(), p, false, &out); err != nil {
			t.Fatal(err)
		}
		if out.String() != "line1\nline2\n" {
			t.Errorf("streamed %q, want the full file", out.String())
		}
	})

	t.Run("missing file errors", func(t *testing.T) {
		var out bytes.Buffer
		if err := streamFile(context.Background(), filepath.Join(t.TempDir(), "absent"), false, &out); err == nil {
			t.Error("missing log must error, not stream nothing")
		}
	})
}
