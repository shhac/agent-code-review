package store

import "testing"

// The log key is a URL token users can hold on to (the review-log page is
// linked by it), so its inputs are a compatibility surface: changing which
// fields feed the hash silently breaks every link already handed out.
func TestReviewLogKeyIgnoresTheTokenSplit(t *testing.T) {
	base := Review{Repo: "o/r", Number: 7, HeadSHA: "abc", Verdict: VerdictApproved,
		Engine: "claude", TokensUsed: 3_700_000}
	split := base
	split.FreshTokens, split.CacheReadTokens = 250_000, 3_450_000

	if ReviewLogKey(base) != ReviewLogKey(split) {
		t.Error("recording the split changed the log key, breaking existing review-log links")
	}
}
