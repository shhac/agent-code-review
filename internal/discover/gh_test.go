package discover

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/agent-code-review/internal/store"
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

// GitHub logins are case-insensitive and gh_user is a hand-typed override, so
// "Review-Bot" configured against GitHub's "review-bot" is the same account. A
// case-sensitive match let a re-claim miss its own posted review and review
// (and post) again.
func TestAlreadyReviewedByIgnoresLoginCase(t *testing.T) {
	cases := []struct {
		name, ours, theirs string
		want               bool
	}{
		{"same case", "review-bot", "review-bot", true},
		{"configured with capitals", "Review-Bot", "review-bot", true},
		{"GitHub reports capitals", "review-bot", "REVIEW-BOT", true},
		{"a different account", "review-bot", "review-bot2", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr := ghPR{Reviews: []ghReview{{State: "COMMENTED", Author: ghActor{Login: tc.theirs}}}}
			pr.Reviews[0].Commit.OID = "headsha"
			if got := pr.AlreadyReviewedBy(tc.ours, "headsha"); got != tc.want {
				t.Errorf("AlreadyReviewedBy(%q) over a review by %q = %v, want %v", tc.ours, tc.theirs, got, tc.want)
			}
		})
	}
}

// ghCalls reads back the argv of every fake gh invocation, one slice per call.
// The fake appends each argument on its own line and a "--end--" after them.
func ghCalls(t *testing.T) [][]string {
	t.Helper()
	raw, err := os.ReadFile(ghDir + "/args")
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	var cur []string
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if line == "--end--" {
			calls = append(calls, cur)
			cur = nil
			continue
		}
		cur = append(cur, line)
	}
	return calls
}

const recordArgs = `for a in "$@"; do printf '%s\n' "$a" | head -1 >> "$(dirname "$STATE")/args"; done; echo --end-- >> "$(dirname "$STATE")/args"`

// PRFiles walks the files connection page by page. The first page must OMIT
// the cursor variable (gh has no spelling for a null String), and every page's
// files must land in one list.
func TestPRFilesPagesThroughTheCursor(t *testing.T) {
	restore := fakeGH(t, recordArgs+`
case "$*" in
*cursor=c1*) echo '{"data":{"repository":{"pullRequest":{"headRefOid":"sha","additions":3,"deletions":1,"changedFiles":2,
  "files":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"path":"b.go","additions":1,"deletions":1}]}}}}}' ;;
*) echo '{"data":{"repository":{"pullRequest":{"headRefOid":"sha","additions":3,"deletions":1,"changedFiles":2,
  "files":{"pageInfo":{"hasNextPage":true,"endCursor":"c1"},"nodes":[{"path":"a.go","additions":2}]}}}}}' ;;
esac`)
	defer restore()

	got, err := PRFiles(context.Background(), "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if got.HeadSHA != "sha" || got.Additions != 3 || got.Deletions != 1 || got.ChangedFiles != 2 || got.Truncated {
		t.Errorf("totals = %+v", got)
	}
	if len(got.Files) != 2 || got.Files[0].Path != "a.go" || got.Files[1].Path != "b.go" {
		t.Errorf("files = %+v, want a.go then b.go", got.Files)
	}
	calls := ghCalls(t)
	if len(calls) != 2 {
		t.Fatalf("gh called %d times, want 2", len(calls))
	}
	for _, arg := range calls[0] {
		if strings.HasPrefix(arg, "cursor=") {
			t.Errorf("the first page passed %q; it must omit the cursor entirely", arg)
		}
	}
	if !slices.Contains(calls[0], "owner=o") || !slices.Contains(calls[0], "repo=r") || !slices.Contains(calls[0], "number=7") {
		t.Errorf("first call argv = %q, want owner, repo and number variables", calls[0])
	}
	if !slices.Contains(calls[1], "cursor=c1") {
		t.Errorf("second call argv = %q, want the first page's end cursor", calls[1])
	}
}

// GitAttributes asks for one alias per candidate directory and keeps only the
// blobs that exist.
func TestGitAttributesMapsAliasesBackToDirectories(t *testing.T) {
	restore := fakeGH(t, recordArgs+`
echo '{"data":{"repository":{"a0":{"text":"*.pb.go linguist-generated"},"a1":null}}}'`)
	defer restore()

	got, err := GitAttributes(context.Background(), "o/r", "", files("gen/x.pb.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[""] != "*.pb.go linguist-generated" {
		t.Errorf("attrs = %q, want only the root file", got)
	}
	calls := ghCalls(t)
	if len(calls) != 1 || !slices.Contains(calls[0], "e0=HEAD:.gitattributes") || !slices.Contains(calls[0], "e1=HEAD:gen/.gitattributes") {
		t.Errorf("argv = %q, want one expression per directory at HEAD", calls)
	}
}

// Every gh read names what it failed to decode, so a log line says which
// call GitHub answered strangely rather than just "invalid character".
func TestGHReadsNameWhatFailedToDecode(t *testing.T) {
	restore := fakeGH(t, `echo 'not json'`)
	defer restore()
	ctx := context.Background()

	_, prFilesErr := PRFiles(ctx, "o/r", 7)
	_, attrsErr := GitAttributes(ctx, "o/r", "HEAD", files("a.go"))
	_, activityErr := LastHumanActivity(ctx, "o/r", 7, "us")
	_, manualErr := ManualCandidate(ctx, "o/r", 7)
	_, _, recheckErr := StillCandidateAt(ctx, "o/r", 7, "", "", false)

	for _, tc := range []struct {
		err  error
		want string
	}{
		{prFilesErr, "decode pr files for o/r#7: "},
		{attrsErr, "decode gitattributes for o/r: "},
		{activityErr, "decode human activity for o/r#7: "},
		{manualErr, "parse gh pr view: "},
		{recheckErr, "parse gh pr view: "},
	} {
		if tc.err == nil || !strings.HasPrefix(tc.err.Error(), tc.want) {
			t.Errorf("err = %v, want it to start %q", tc.err, tc.want)
		}
	}
}

// Both `gh pr view` reads name the PR and the fields they need, and the
// manual add maps what comes back.
func TestPRViewReadsAskForTheirFields(t *testing.T) {
	restore := fakeGH(t, recordArgs+`
echo '{"number":7,"title":"t","author":{"login":"alice"},"headRefOid":"sha","state":"OPEN","isDraft":false}'`)
	defer restore()
	ctx := context.Background()

	c, err := ManualCandidate(ctx, "o/r", 7)
	if err != nil || c.HeadSHA != "sha" || c.Author != "alice" || c.Source != store.SourceManual {
		t.Fatalf("ManualCandidate = %+v, %v", c, err)
	}
	if ok, _, err := StillCandidateAt(ctx, "o/r", 7, "", "", false); err != nil || !ok {
		t.Fatalf("StillCandidateAt = %v, %v", ok, err)
	}
	// Lowercased because prune compares it to "open": gh's OPEN passed through
	// as-is would report every open manual add as merged or closed.
	if state, err := PRState(ctx, "o/r", 7); err != nil || state != "open" {
		t.Fatalf("PRState = %q, %v; want open", state, err)
	}
	calls := ghCalls(t)
	if len(calls) != 3 {
		t.Fatalf("gh called %d times, want 3", len(calls))
	}
	for i, fields := range []string{
		"title,author,url,headRefOid,state,createdAt,updatedAt,additions,deletions,changedFiles",
		"number,isDraft,state,reviewRequests,reviewDecision,reviews,headRefOid",
		"state",
	} {
		want := []string{"pr", "view", "7", "--repo", "o/r", "--json", fields}
		if !slices.Equal(calls[i], want) {
			t.Errorf("call %d argv = %q, want %q", i, calls[i], want)
		}
	}
}

// A discussion candidate is a re-review of a head we already reviewed, so
// passing that head would make the already-reviewed guard reject every one.
func TestRecheckHead(t *testing.T) {
	for _, tc := range []struct {
		typ  string
		want string
	}{
		{store.TypeNew, "sha1"},
		{store.TypeRefreshed, "sha1"},
		{store.TypeDiscussion, ""},
	} {
		if got := RecheckHead(store.Candidate{Type: tc.typ, HeadSHA: "sha1"}); got != tc.want {
			t.Errorf("RecheckHead(%s) = %q, want %q", tc.typ, got, tc.want)
		}
	}
}
