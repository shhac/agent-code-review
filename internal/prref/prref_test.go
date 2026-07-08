package prref

import "testing"

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

func TestParseArgs(t *testing.T) {
	ref, err := ParseArgs([]string{"o/r", "7"})
	if err != nil || ref != (Ref{Repo: "o/r", Number: 7}) {
		t.Fatalf("ParseArgs valid = %v, %v", ref, err)
	}
	for _, args := range [][]string{{"not-a-repo", "7"}, {"o/r", "wat"}, {"o/r", "0"}, {"o/r", "-1"}} {
		if _, err := ParseArgs(args); err == nil {
			t.Fatalf("ParseArgs(%v) expected error", args)
		}
	}
}
