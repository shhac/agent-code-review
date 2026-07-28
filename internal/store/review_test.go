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

// Only claude values its own runs, so without the estimate fallback every
// codex review reads as free. The reported figure must still win where it
// exists: our rates are an inference, the engine's is not.
func TestEffectiveCostPrefersTheReportedFigure(t *testing.T) {
	reported := Review{CostUSD: 3.71, EstCostUSD: 3.20}
	if got := reported.EffectiveCostUSD(); got != 3.71 {
		t.Errorf("effective = %v, want the engine's own 3.71", got)
	}
	if reported.CostEstimated() {
		t.Error("a row with a reported cost must not be marked estimated")
	}

	estimated := Review{EstCostUSD: 0.42}
	if got := estimated.EffectiveCostUSD(); got != 0.42 {
		t.Errorf("effective = %v, want the estimate 0.42", got)
	}
	if !estimated.CostEstimated() {
		t.Error("a row priced only by us must be marked estimated")
	}

	// Neither figure means unknown. It must stay 0 AND stay unmarked, so an
	// aggregate can exclude it rather than count a free review.
	unknown := Review{}
	if unknown.EffectiveCostUSD() != 0 || unknown.CostEstimated() {
		t.Error("a row with no spend figure must read as unknown, not as an estimate of zero")
	}
}
