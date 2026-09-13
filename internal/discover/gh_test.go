package discover

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestCandidateFromView pins the manual-add path: `gh pr view` JSON
// unmarshals into the shared ghPR wire shape, the open-only gate fires, and
// the field mapping reaches the candidate.
func TestCandidateFromView(t *testing.T) {
	out := []byte(`{
		"title": "fix: a thing",
		"author": {"login": "alice"},
		"url": "https://github.com/o/r/pull/7",
		"headRefOid": "abc123",
		"state": "OPEN",
		"createdAt": "2026-07-01T10:00:00Z",
		"updatedAt": "2026-07-02T11:00:00Z"
	}`)
	var pr ghPR
	if err := json.Unmarshal(out, &pr); err != nil {
		t.Fatal(err)
	}
	c, err := candidateFromView("o/r", 7, pr)
	if err != nil {
		t.Fatal(err)
	}
	if c.Title != "fix: a thing" || c.Author != "alice" || c.HeadSHA != "abc123" ||
		c.URL != "https://github.com/o/r/pull/7" || c.Type != "new" || c.Source != "manual" {
		t.Errorf("mapping wrong: %+v", c)
	}
	if c.CreatedAt.IsZero() || c.UpdatedAt.IsZero() {
		t.Error("timestamps not parsed")
	}
	if c.DiscoveredAt.IsZero() {
		t.Error("DiscoveredAt must be stamped")
	}

	for _, state := range []string{"MERGED", "CLOSED", ""} {
		_, err := candidateFromView("o/r", 7, ghPR{State: state})
		if err == nil {
			t.Errorf("state %q must be rejected", state)
		} else if state != "" && !strings.Contains(err.Error(), state) {
			t.Errorf("error should name the state, got: %v", err)
		}
	}
}

// TestStillCandidateFromJSON covers the pre-review recheck's decision table:
// the live-state gate unique to this path, delegation to the shared candidacy
// gates, and the malformed-payload error.
func TestStillCandidateFromJSON(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		ok      bool
		reason  string
		wantErr bool
	}{
		{"still a candidate", `{"state":"OPEN","isDraft":false,"reviewRequests":[{"login":"reviewer"}],"reviewDecision":"REVIEW_REQUIRED"}`, true, "", false},
		{"merged", `{"state":"MERGED"}`, false, "merged", false},
		{"closed", `{"state":"CLOSED"}`, false, "closed", false},
		{"turned draft", `{"state":"OPEN","isDraft":true,"reviewRequests":[{"login":"reviewer"}]}`, false, "draft", false},
		{"request withdrawn", `{"state":"OPEN","isDraft":false,"reviewRequests":[]}`, false, "no open review request", false},
		{"approved meanwhile", `{"state":"OPEN","isDraft":false,"reviewRequests":[{"login":"reviewer"}],"reviewDecision":"APPROVED"}`, false, "already approved", false},
		{"malformed payload", `not json`, false, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason, err := stillCandidateFromJSON([]byte(tc.json), "", "", true)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected parse error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if ok != tc.ok || reason != tc.reason {
				t.Errorf("got ok=%v reason=%q, want ok=%v reason=%q", ok, reason, tc.ok, tc.reason)
			}
		})
	}
}

// An attempt interrupted AFTER it posted its review recorded nothing, so on
// re-claim nothing downstream knows the work is done. Until now this was
// caught only as a side effect: GitHub clears the review request when a
// requested reviewer submits, so the candidacy gate happened to reject the PR.
// That is incidental, and it does not hold when the request came from a team
// or was re-added.
func TestStillCandidateSkipsWhatWeAlreadyReviewedAtThisHead(t *testing.T) {
	const payload = `{"number":7,"state":"OPEN","isDraft":false,
	  "reviewRequests":[{"login":"bot"}],"reviewDecision":"REVIEW_REQUIRED",
	  "headRefOid":"headsha",
	  "reviews":[{"state":"COMMENTED","author":{"login":"bot"},"commit":{"oid":"headsha"}}]}`

	ok, reason, err := stillCandidateFromJSON([]byte(payload), "bot", "headsha", true)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("a PR we already reviewed at this exact head must not be reviewed again")
	}
	if reason != "already reviewed at this revision" {
		t.Errorf("reason = %q, want it to name the real cause rather than a missing request", reason)
	}

	// New commits since our review: a different head, so there is real work.
	ok, _, err = stillCandidateFromJSON([]byte(payload), "bot", "newersha", true)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("a newer head must still be a candidate; the guard is per revision, not per PR")
	}

	// Someone else's review at this head is not ours and blocks nothing.
	other := `{"number":7,"state":"OPEN","isDraft":false,
	  "reviewRequests":[{"login":"bot"}],"reviewDecision":"REVIEW_REQUIRED",
	  "headRefOid":"headsha",
	  "reviews":[{"state":"COMMENTED","author":{"login":"someone-else"},"commit":{"oid":"headsha"}}]}`
	ok, _, err = stillCandidateFromJSON([]byte(other), "bot", "headsha", true)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("another reviewer's review must not count as ours")
	}

	// Without an identity to compare against, the guard must not fire at all
	// rather than guess.
	ok, _, err = stillCandidateFromJSON([]byte(payload), "", "headsha", true)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("no login means the guard cannot apply, not that everything is already reviewed")
	}
}

// --- transient-failure retry ---

func TestTransientClassification(t *testing.T) {
	retry := []string{
		"gh pr list: HTTP 502: 502 Bad Gateway (https://api.github.com/graphql)",
		"gh api: HTTP 503: Service Unavailable",
		"gh pr list: HTTP 504: Gateway Timeout",
		"gh api: read tcp: connection reset by peer",
		"gh api: net/http: TLS handshake timeout",
	}
	for _, msg := range retry {
		if !transient(msg) {
			t.Errorf("transient(%q) = false, want true", msg)
		}
	}
	// A 4xx, a bad query, or a missing repo fails identically however many
	// times we ask; retrying only delays the error.
	permanent := []string{
		"gh pr list: HTTP 404: Not Found",
		"gh pr list: HTTP 401: Bad credentials",
		"gh api: GraphQL: Field 'nope' doesn't exist",
		"gh pr list: could not resolve to a Repository",
	}
	for _, msg := range permanent {
		if transient(msg) {
			t.Errorf("transient(%q) = true, want false", msg)
		}
	}
}

func TestRunGHRetriesTransientThenSucceeds(t *testing.T) {
	restore := fakeGH(t, `
if [ ! -f "$STATE" ]; then echo 1 > "$STATE"; echo "HTTP 502: 502 Bad Gateway" >&2; exit 1; fi
echo '[]'
`)
	defer restore()

	out, err := runGH(context.Background(), "pr", "list")
	if err != nil {
		t.Fatalf("runGH after one 502 = %v, want success on the retry", err)
	}
	if strings.TrimSpace(string(out)) != "[]" {
		t.Fatalf("runGH returned %q, want the retry's output", out)
	}
}

func TestRunGHGivesUpAfterAttempts(t *testing.T) {
	restore := fakeGH(t, `echo "HTTP 502: 502 Bad Gateway" >&2; exit 1`)
	defer restore()

	if _, err := runGH(context.Background(), "pr", "list"); err == nil {
		t.Fatal("runGH with a permanently 502ing gh = nil, want the last error")
	}
	if got := attempts(t); got != ghAttempts {
		t.Fatalf("gh invoked %d times, want %d", got, ghAttempts)
	}
}

func TestRunGHDoesNotRetryPermanentFailure(t *testing.T) {
	restore := fakeGH(t, `echo "HTTP 404: Not Found" >&2; exit 1`)
	defer restore()

	if _, err := runGH(context.Background(), "pr", "list"); err == nil {
		t.Fatal("runGH on a 404 = nil, want an error")
	}
	if got := attempts(t); got != 1 {
		t.Fatalf("gh invoked %d times on a 404, want 1 (no retries)", got)
	}
}

// fakeGH puts a stub `gh` on PATH running body, with $STATE pointing at a
// scratch file the body may use to vary its behaviour between invocations,
// and $COUNT at a tally of invocations. It zeroes the retry delay so the
// retry path costs the suite nothing.
func fakeGH(t *testing.T, body string) func() {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nSTATE=" + dir + "/state\nprintf x >> " + dir + "/count\n" + body + "\n"
	if err := os.WriteFile(dir+"/gh", []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	oldDelay := ghRetryDelay
	ghRetryDelay = 0
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ghDir = dir
	return func() { ghRetryDelay = oldDelay }
}

var ghDir string

func attempts(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile(ghDir + "/count")
	if err != nil {
		return 0
	}
	return len(b)
}
