package discover

import (
	"encoding/json"
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
			ok, reason, err := stillCandidateFromJSON([]byte(tc.json))
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
