package prref

import (
	"errors"
	"testing"
)

func TestParseGitHubPull(t *testing.T) {
	cases := []struct {
		in         string
		repo       string
		number     int
		shouldFail bool
	}{
		{"https://github.com/owner/repo/pull/123", "owner/repo", 123, false},
		{"https://github.com/owner/repo/pull/123/files", "owner/repo", 123, false},
		{"owner/repo/pull/9", "owner/repo", 9, false},
		{"owner/my.repo-x_1/pull/42", "owner/my.repo-x_1", 42, false},
		{"http://github.com/owner/repo/pull/1", "", 0, true},
		{"https://gitlab.com/owner/repo/pull/1", "", 0, true},
		{"owner/bad repo/pull/1", "", 0, true},
		{"owner/repo#123", "", 0, true},
		{"owner/repo", "", 0, true},
		{"just words", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := ParseGitHubPull(tc.in)
			if tc.shouldFail {
				if ok {
					t.Errorf("expected no match, got %v", got)
				}
				return
			}
			if !ok {
				t.Fatal("expected match, got none")
			}
			if got.Repo != tc.repo || got.Number != tc.number {
				t.Errorf("got repo=%s number=%d, want %s %d", got.Repo, got.Number, tc.repo, tc.number)
			}
		})
	}
}

func TestParse(t *testing.T) {
	ref, err := Parse("o/r", "7")
	if err != nil || ref != (Ref{Repo: "o/r", Number: 7}) {
		t.Fatalf("Parse valid = %v, %v", ref, err)
	}
	cases := []struct {
		repo, number string
		want         error
	}{
		{"not-a-repo", "7", ErrRepo},
		{"o/r", "wat", ErrNumber},
		{"o/r", "0", ErrNumber},
		{"o/r", "-1", ErrNumber},
		{"not-a-repo", "wat", ErrRepo}, // both invalid: repo error wins
	}
	for _, tc := range cases {
		if _, err := Parse(tc.repo, tc.number); !errors.Is(err, tc.want) {
			t.Fatalf("Parse(%q, %q) = %v, want %v", tc.repo, tc.number, err, tc.want)
		}
	}
}
