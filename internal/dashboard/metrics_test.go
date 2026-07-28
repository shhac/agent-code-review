package dashboard

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/store"
)

func TestMetricsForFiltersAndGroupsReviewProvenance(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	reviews := []store.Review{
		{Model: "gpt-5.5", Effort: "high", EngineVersion: "Codex CLI 0.144.0", Verdict: "APPROVED", TokensUsed: 100, FreshTokens: 100, DurationSecs: 20, ReviewedAt: now},
		{Model: "gpt-5.5", Effort: "high", EngineVersion: "Codex CLI 0.144.0", Verdict: "COMMENTED", TokensUsed: 300, FreshTokens: 300, DurationSecs: 40, ReviewedAt: now.Add(2 * time.Hour)},
		{Model: "gpt-5.6-terra", Effort: "medium", EngineVersion: "Codex CLI 0.145.0", Verdict: "REQUESTED_CHANGES", TokensUsed: 200, FreshTokens: 200, DurationSecs: 60, ReviewedAt: now},
	}
	got := metricsFor(reviews, "gpt-5.5", "high")
	if got.Summary.Reviews != 2 || got.Summary.FreshTokens != 400 || got.Summary.MedianDuration != 40 {
		t.Errorf("summary = %+v", got.Summary)
	}
	if got.Verdicts["APPROVED"] != 1 || got.Verdicts["COMMENTED"] != 1 || got.Verdicts["REQUESTED_CHANGES"] != 0 {
		t.Errorf("verdicts = %+v", got.Verdicts)
	}
	if len(got.Models) != 1 || got.Models[0].EngineVersion != "Codex CLI 0.144.0" || got.Models[0].MedianDuration != 40 {
		t.Errorf("models = %+v", got.Models)
	}
	if len(got.Activity) != 1 || got.Activity[0].Reviews != 2 || len(got.Scatter) != 2 {
		t.Errorf("activity/scatter = %+v / %+v", got.Activity, got.Scatter)
	}
}

func TestMetricsForBucketsDaysAndSortsGroups(t *testing.T) {
	west := time.FixedZone("west", -5*3600)
	reviews := []store.Review{
		// group A (gpt-5.5/high/v1): 2 reviews on 07-08.
		{Model: "gpt-5.5", Effort: "high", EngineVersion: "v1", Verdict: "APPROVED", TokensUsed: 100, FreshTokens: 100, DurationSecs: 10, ReviewedAt: time.Date(2026, 7, 8, 9, 0, 0, 0, time.UTC)},
		{Model: "gpt-5.5", Effort: "high", EngineVersion: "v1", Verdict: "COMMENTED", TokensUsed: 100, FreshTokens: 100, DurationSecs: 30, ReviewedAt: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)},
		// group B (gpt-5.6/medium/v2): 1 review whose western-evening local time rolls into 07-09 UTC.
		{Model: "gpt-5.6", Effort: "medium", EngineVersion: "v2", Verdict: "APPROVED", TokensUsed: 50, FreshTokens: 50, DurationSecs: 20, ReviewedAt: time.Date(2026, 7, 8, 22, 0, 0, 0, west)},
		// group C (gpt-6/low/v3): 3 reviews on 07-10: the largest group.
		{Model: "gpt-6", Effort: "low", EngineVersion: "v3", Verdict: "APPROVED", TokensUsed: 10, FreshTokens: 10, DurationSecs: 5, ReviewedAt: time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)},
		{Model: "gpt-6", Effort: "low", EngineVersion: "v3", Verdict: "SKIPPED", TokensUsed: 10, FreshTokens: 10, DurationSecs: 5, ReviewedAt: time.Date(2026, 7, 10, 2, 0, 0, 0, time.UTC)},
		{Model: "gpt-6", Effort: "low", EngineVersion: "v3", Verdict: "APPROVED", TokensUsed: 10, FreshTokens: 10, DurationSecs: 5, ReviewedAt: time.Date(2026, 7, 10, 3, 0, 0, 0, time.UTC)},
	}
	got := metricsFor(reviews, "", "")

	// Activity is ascending by UTC day; the western-evening review buckets into 07-09.
	wantDays := []string{"2026-07-08", "2026-07-09", "2026-07-10"}
	if len(got.Activity) != len(wantDays) {
		t.Fatalf("activity = %+v", got.Activity)
	}
	for i, d := range got.Activity {
		if d.Day != wantDays[i] {
			t.Errorf("activity[%d].Day = %s, want %s", i, d.Day, wantDays[i])
		}
	}
	if got.Activity[0].Reviews != 2 || got.Activity[1].Reviews != 1 || got.Activity[2].Reviews != 3 {
		t.Errorf("per-day reviews = %+v", got.Activity)
	}

	// Models is descending by review count: C(3) > A(2) > B(1).
	if len(got.Models) != 3 {
		t.Fatalf("models = %+v", got.Models)
	}
	if got.Models[0].Reviews != 3 || got.Models[1].Reviews != 2 || got.Models[2].Reviews != 1 {
		t.Errorf("models not sorted desc by reviews: %+v", got.Models)
	}
	if got.Models[0].Model != "gpt-6" || got.Models[2].Model != "gpt-5.6" {
		t.Errorf("model order = %+v", got.Models)
	}
}

func TestMetricsForEmptyInputKeepsNonNilSlices(t *testing.T) {
	// Empty input and an all-excluding filter must both preserve the API's
	// non-nil-slice contract: the Svelte client relies on activity/models/scatter
	// marshalling to [] (not null) so its `data?.x || []` reads stay arrays.
	filtered := metricsFor([]store.Review{{Model: "gpt-5.5", ReviewedAt: time.Now()}}, "no-such-model", "")
	for name, got := range map[string]metricsResp{"nil": metricsFor(nil, "", ""), "all-filtered": filtered} {
		if got.Summary.Reviews != 0 || got.Summary.FreshTokens != 0 || got.Summary.MedianDuration != 0 {
			t.Errorf("%s: summary = %+v", name, got.Summary)
		}
		blob, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, want := range []string{`"activity":[]`, `"models":[]`, `"scatter":[]`} {
			if !strings.Contains(string(blob), want) {
				t.Errorf("%s: expected %s in %s", name, want, blob)
			}
		}
	}
}

func TestMetricsSinceDefaultsToThirtyDays(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	if got := metricsSince("nonsense", now); !got.Equal(time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("default start = %s", got)
	}
}

// The cost aggregates are what a per-review budget gets set from, so the
// arithmetic and the zero-cost exclusion both need pinning. codex reports no
// cost, so its rows arrive as 0; folding those into the median would halve it
// on a mixed history and produce a budget far too tight.
func TestMetricsCostAggregatesIgnoreUnpricedReviews(t *testing.T) {
	now := time.Now()
	got := metricsFor([]store.Review{
		{Model: "claude-opus-5", Effort: "medium", Verdict: "APPROVED", CostUSD: 0.40, ReviewedAt: now},
		{Model: "claude-opus-5", Effort: "medium", Verdict: "COMMENTED", CostUSD: 0.60, ReviewedAt: now},
		{Model: "claude-opus-5", Effort: "medium", Verdict: "APPROVED", CostUSD: 3.00, ReviewedAt: now},
		// codex rows: priced at 0 because the engine reports no cost.
		{Model: "gpt-5.6", Effort: "high", Verdict: "APPROVED", CostUSD: 0, ReviewedAt: now},
		{Model: "gpt-5.6", Effort: "high", Verdict: "APPROVED", CostUSD: 0, ReviewedAt: now},
	}, "", "")

	// Total spans everything (unpriced rows contribute 0 honestly).
	if got.Summary.CostUSD != 4.0 {
		t.Errorf("total cost = %v, want 4.0", got.Summary.CostUSD)
	}
	// Median and peak consider only priced rows: 0.40/0.60/3.00 -> 0.60.
	// Including the two zeros would give 0.40, a materially tighter budget.
	if got.Summary.MedianCostUSD != 0.60 {
		t.Errorf("median cost = %v, want 0.60 (zero-cost rows must be excluded)", got.Summary.MedianCostUSD)
	}
	if got.Summary.MaxCostUSD != 3.00 {
		t.Errorf("peak cost = %v, want 3.00", got.Summary.MaxCostUSD)
	}

	// Per-model: the unpriced group reports no median rather than a fake 0.
	byModel := map[string]modelMetric{}
	for _, m := range got.Models {
		byModel[m.Model] = m
	}
	if byModel["claude-opus-5"].MedianCostUSD != 0.60 {
		t.Errorf("claude median = %v, want 0.60", byModel["claude-opus-5"].MedianCostUSD)
	}
	if byModel["gpt-5.6"].MedianCostUSD != 0 {
		t.Errorf("unpriced model median = %v, want 0", byModel["gpt-5.6"].MedianCostUSD)
	}
}

// A history with no priced reviews at all must report no median, not a
// division-by-zero or a spurious figure.
func TestMetricsCostAggregatesWithNothingPriced(t *testing.T) {
	got := metricsFor([]store.Review{
		{Model: "gpt-5.6", Verdict: "APPROVED", CostUSD: 0, ReviewedAt: time.Now()},
	}, "", "")
	if got.Summary.MedianCostUSD != 0 || got.Summary.MaxCostUSD != 0 || got.Summary.CostUSD != 0 {
		t.Errorf("summary = %+v, want all-zero cost", got.Summary)
	}
}

// Charting raw totals compared two different measurements: claude counts
// cached re-reads (millions per review), codex reports a single figure that
// doesn't. Every cross-engine token aggregate must use the comparable one.
func TestMetricsChartsFreshTokensNotRawTotals(t *testing.T) {
	now := time.Now()
	got := metricsFor([]store.Review{
		// A claude review: 3.7M total, but only 250k actually processed.
		{Model: "claude-opus-5", Verdict: "APPROVED", ReviewedAt: now,
			TokensUsed: 3_700_000, FreshTokens: 250_000, CacheReadTokens: 3_450_000},
		// A codex review: its whole total is fresh.
		{Model: "gpt-5.6", Verdict: "APPROVED", ReviewedAt: now,
			TokensUsed: 130_000, FreshTokens: 130_000},
	}, "", "")

	// 250k + 130k, not 3.7M + 130k.
	if got.Summary.FreshTokens != 380_000 {
		t.Errorf("summary tokens = %d, want 380000 (cached re-reads excluded)", got.Summary.FreshTokens)
	}
	if len(got.Activity) != 1 || got.Activity[0].FreshTokens != 380_000 {
		t.Errorf("activity = %+v, want the same comparable figure", got.Activity)
	}
	byModel := map[string]int{}
	for _, m := range got.Models {
		byModel[m.Model] = m.FreshTokens
	}
	if byModel["claude-opus-5"] != 250_000 {
		t.Errorf("claude model tokens = %d, want 250000", byModel["claude-opus-5"])
	}
	// codex records its total as fresh, or it would chart as zero.
	if byModel["gpt-5.6"] != 130_000 {
		t.Errorf("codex model tokens = %d, want its reported total 130000", byModel["gpt-5.6"])
	}
	for _, p := range got.Scatter {
		if p.Model == "claude-opus-5" && p.FreshTokens != 250_000 {
			t.Errorf("scatter point = %d, want the comparable figure", p.FreshTokens)
		}
	}
}
