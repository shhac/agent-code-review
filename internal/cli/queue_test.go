package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

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
