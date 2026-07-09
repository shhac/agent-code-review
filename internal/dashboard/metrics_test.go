package dashboard

import (
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/store"
)

func TestMetricsForFiltersAndGroupsReviewProvenance(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	reviews := []store.Review{
		{Model: "gpt-5.5", Effort: "high", CodexVersion: "Codex CLI 0.144.0", Verdict: "APPROVED", TokensUsed: 100, DurationSecs: 20, ReviewedAt: now},
		{Model: "gpt-5.5", Effort: "high", CodexVersion: "Codex CLI 0.144.0", Verdict: "COMMENTED", TokensUsed: 300, DurationSecs: 40, ReviewedAt: now.Add(2 * time.Hour)},
		{Model: "gpt-5.6-terra", Effort: "medium", CodexVersion: "Codex CLI 0.145.0", Verdict: "REQUESTED_CHANGES", TokensUsed: 200, DurationSecs: 60, ReviewedAt: now},
	}
	got := metricsFor(reviews, "gpt-5.5", "high")
	if got.Summary.Reviews != 2 || got.Summary.TokensUsed != 400 || got.Summary.MedianDuration != 40 {
		t.Errorf("summary = %+v", got.Summary)
	}
	if got.Verdicts["APPROVED"] != 1 || got.Verdicts["COMMENTED"] != 1 || got.Verdicts["REQUESTED_CHANGES"] != 0 {
		t.Errorf("verdicts = %+v", got.Verdicts)
	}
	if len(got.Models) != 1 || got.Models[0].CodexVersion != "Codex CLI 0.144.0" || got.Models[0].MedianDuration != 40 {
		t.Errorf("models = %+v", got.Models)
	}
	if len(got.Activity) != 1 || got.Activity[0].Reviews != 2 || len(got.Scatter) != 2 {
		t.Errorf("activity/scatter = %+v / %+v", got.Activity, got.Scatter)
	}
}

func TestMetricsSinceDefaultsToThirtyDays(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	if got := metricsSince("nonsense", now); !got.Equal(time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("default start = %s", got)
	}
}
