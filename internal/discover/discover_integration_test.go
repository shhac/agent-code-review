//go:build integration

package discover

import (
	"context"
	"os"
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
)

// TestLiveDiscovery runs the real `gh pr list` path against a repo named in
// AGENT_CODE_REVIEW_TEST_REPO (never hardcoded) and classifies the results.
// Validates that the JSON fields we request unmarshal against live data.
func TestLiveDiscovery(t *testing.T) {
	repo := os.Getenv("AGENT_CODE_REVIEW_TEST_REPO")
	if repo == "" {
		t.Skip("AGENT_CODE_REVIEW_TEST_REPO not set")
	}

	d := New(config.Config{Repos: []string{repo}}, &fakeStore{}, t.Logf)
	prs, err := d.listPRs(context.Background(), repo)
	if err != nil {
		t.Fatalf("listPRs: %v", err)
	}
	t.Logf("%s: %d open PRs fetched", repo, len(prs))

	for _, pr := range prs {
		c, ok, err := d.classify(context.Background(), repo, pr)
		if err != nil {
			t.Fatalf("classify #%d: %v", pr.Number, err)
		}
		if ok {
			t.Logf("candidate: #%d %s type=%s author=%s sha=%.8s", c.Number, c.Title, c.Type, c.Author, c.HeadSHA)
			if c.HeadSHA == "" || c.Author == "" {
				t.Errorf("#%d: candidate missing head SHA or author", c.Number)
			}
		}
	}
}
