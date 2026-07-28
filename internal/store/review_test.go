package store

import "testing"

// FreshTokens is the cross-engine comparable figure; the fallback for an
// engine that reports no split is what keeps codex from charting as zero.
func TestFreshTokens(t *testing.T) {
	split := Review{TokensUsed: 3_700_000, InputTokens: 40_000, OutputTokens: 60_000,
		CacheCreationTokens: 150_000, CacheReadTokens: 3_450_000}
	if got := split.FreshTokens(); got != 250_000 {
		t.Errorf("with a split: %d, want 250000 (cached reads excluded)", got)
	}
	totalOnly := Review{TokensUsed: 130_000}
	if got := totalOnly.FreshTokens(); got != 130_000 {
		t.Errorf("without a split: %d, want the reported total", got)
	}
	if got := (Review{}).FreshTokens(); got != 0 {
		t.Errorf("empty review: %d, want 0", got)
	}
}
