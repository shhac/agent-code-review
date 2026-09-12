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
	if len(got.Models) != 1 || got.Models[0].MedianDuration != 40 {
		t.Errorf("models = %+v", got.Models)
	}
	// The CLI version is a nested breakdown now, not part of the row's key.
	if v := got.Models[0].Versions; len(v) != 1 || v[0].EngineVersion != "Codex CLI 0.144.0" || v[0].Reviews != 2 {
		t.Errorf("versions = %+v", got.Models[0].Versions)
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
		// group C (gpt-6/low/v3): 3 reviews on 07-10: the largest group. The
		// SKIPPED row alongside them is the point: it is an OUTCOME, not a
		// review, and counting it as one is what made "reviews completed" wrong
		// on the metrics page.
		{Model: "gpt-6", Effort: "low", EngineVersion: "v3", Verdict: "APPROVED", TokensUsed: 10, FreshTokens: 10, DurationSecs: 5, ReviewedAt: time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)},
		{Model: "gpt-6", Effort: "low", EngineVersion: "v3", Verdict: "SKIPPED", TokensUsed: 10, FreshTokens: 10, DurationSecs: 5, ReviewedAt: time.Date(2026, 7, 10, 2, 0, 0, 0, time.UTC)},
		{Model: "gpt-6", Effort: "low", EngineVersion: "v3", Verdict: "APPROVED", TokensUsed: 10, FreshTokens: 10, DurationSecs: 5, ReviewedAt: time.Date(2026, 7, 10, 3, 0, 0, 0, time.UTC)},
		{Model: "gpt-6", Effort: "low", EngineVersion: "v3", Verdict: "APPROVED", TokensUsed: 10, FreshTokens: 10, DurationSecs: 5, ReviewedAt: time.Date(2026, 7, 10, 4, 0, 0, 0, time.UTC)},
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

	// The two counts are reported apart, and the skip is the difference.
	if got.Summary.Reviews != 6 || got.Summary.Outcomes != 7 {
		t.Errorf("reviews = %d, outcomes = %d, want 6 and 7: a skip is an outcome, not a review",
			got.Summary.Reviews, got.Summary.Outcomes)
	}
	// It is still visible where it belongs: the verdict breakdown exists to
	// show skips and errors, so that one must NOT be filtered.
	if got.Verdicts["SKIPPED"] != 1 {
		t.Errorf("verdict counts = %+v, want the skip counted there", got.Verdicts)
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

// Only claude values its own runs. Before estimates, a mixed history's cost
// total described the claude third of it and read every codex review as free.
// The effective figure is what makes the total mean something, and the counts
// beside it are what stop an inferred total passing as a measured one.
func TestMetricsCostUsesEstimatesWhereTheEngineReportedNone(t *testing.T) {
	now := time.Now()
	got := metricsFor([]store.Review{
		// claude: reports its own, and we valued it too.
		{Model: "claude-opus-5", Verdict: "APPROVED", ReviewedAt: now, CostUSD: 4.00, EstCostUSD: 3.60},
		// codex: reports nothing, so ours is the only figure there is.
		{Model: "gpt-5.6", Verdict: "APPROVED", ReviewedAt: now, EstCostUSD: 0.50},
		{Model: "gpt-5.6", Verdict: "APPROVED", ReviewedAt: now, EstCostUSD: 1.50},
		// Priced by neither: unknown, and unknown must not read as free.
		{Model: "gpt-5.6", Verdict: "SKIPPED", ReviewedAt: now},
	}, "", "")

	// 4.00 reported + 0.50 + 1.50 estimated. The reported figure wins on the
	// row that has both, so 3.60 must not appear anywhere in the total.
	if got.Summary.CostUSD != 6.00 {
		t.Errorf("total cost = %v, want 6.00 (reported wins, estimates fill the gap)", got.Summary.CostUSD)
	}
	if got.Summary.PricedReviews != 3 || got.Summary.EstimatedReviews != 2 {
		t.Errorf("priced=%d estimated=%d, want 3 and 2: the unpriced review is in neither",
			got.Summary.PricedReviews, got.Summary.EstimatedReviews)
	}
	// Median over the three priced reviews, never dragged down by the unknown.
	if got.Summary.MedianCostUSD != 1.50 {
		t.Errorf("median = %v, want 1.50 over the priced reviews only", got.Summary.MedianCostUSD)
	}
	if got.Summary.MaxCostUSD != 4.00 {
		t.Errorf("max = %v, want 4.00", got.Summary.MaxCostUSD)
	}

	// The cross-check covers only the review carrying both figures: it is a
	// comparison of the two methods, not a second total.
	if got.Summary.CheckReviews != 1 || got.Summary.CheckReportedUSD != 4.00 || got.Summary.CheckEstimatedUSD != 3.60 {
		t.Errorf("cross-check = %d reviews, reported %v vs estimated %v; want 1, 4.00, 3.60",
			got.Summary.CheckReviews, got.Summary.CheckReportedUSD, got.Summary.CheckEstimatedUSD)
	}
}

// The model+effort row's median must come from ALL its reviews, not from
// averaging the per-version medians: a median of medians is not a median, and
// the two disagree whenever the versions are unevenly used.
func TestModelRowMedianIsNotAMedianOfMedians(t *testing.T) {
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mk := func(version string, secs int) store.Review {
		return store.Review{
			Model: "m", Effort: "high", EngineVersion: version, Verdict: "APPROVED",
			FreshTokens: 10, DurationSecs: secs, ReviewedAt: at,
		}
	}
	// v1 has four fast reviews (median 10), v2 one slow one (median 1000).
	// Averaging those medians gives 505; the true median over all five is 10.
	got := metricsFor([]store.Review{
		mk("v1", 10), mk("v1", 10), mk("v1", 10), mk("v1", 10), mk("v2", 1000),
	}, "", "")

	if len(got.Models) != 1 {
		t.Fatalf("models = %+v, want one row for the single model+effort", got.Models)
	}
	row := got.Models[0]
	if row.Reviews != 5 {
		t.Errorf("reviews = %d, want all 5 on the one row", row.Reviews)
	}
	if row.MedianDuration != 10 {
		t.Errorf("median = %d, want the true median of 10 (a median of medians would give 505)", row.MedianDuration)
	}
	if len(row.Versions) != 2 {
		t.Fatalf("versions = %+v, want both nested", row.Versions)
	}
	// Busiest version first, each with its own real median.
	if row.Versions[0].EngineVersion != "v1" || row.Versions[0].Reviews != 4 || row.Versions[0].MedianDuration != 10 {
		t.Errorf("v1 = %+v", row.Versions[0])
	}
	if row.Versions[1].EngineVersion != "v2" || row.Versions[1].MedianDuration != 1000 {
		t.Errorf("v2 = %+v", row.Versions[1])
	}
}

// One model reviewed by several CLI versions is ONE row, which is the whole
// point of the regrouping.
func TestVersionsCollapseIntoOneModelRow(t *testing.T) {
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	var reviews []store.Review
	for _, v := range []string{"v1", "v2", "v3", "v4"} {
		reviews = append(reviews, store.Review{
			Model: "m", Effort: "high", EngineVersion: v, Verdict: "APPROVED",
			FreshTokens: 5, DurationSecs: 10, ReviewedAt: at,
		})
	}
	got := metricsFor(reviews, "", "")
	if len(got.Models) != 1 {
		t.Fatalf("got %d rows, want 1: the version is a breakdown, not a key", len(got.Models))
	}
	if got.Models[0].FreshTokens != 20 || len(got.Models[0].Versions) != 4 {
		t.Errorf("row = %+v, want the four versions summed and nested", got.Models[0])
	}
}

// The sort tie-breaks exist so identical requests return identical orderings.
// Every other fixture uses distinct review counts, so nothing exercised them:
// collapsing them back to a bare Reviews comparison would reintroduce
// map-iteration nondeterminism with no test noticing.
func TestModelGroupsOrderingIsStableOnTies(t *testing.T) {
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mk := func(model, effort, version string) store.Review {
		return store.Review{
			Model: model, Effort: effort, EngineVersion: version, Verdict: "APPROVED",
			FreshTokens: 1, DurationSecs: 1, ReviewedAt: at,
		}
	}
	// Two model rows tied at 2 reviews, and within one row two versions tied at 1.
	reviews := []store.Review{
		mk("zeta", "high", "v1"), mk("zeta", "high", "v2"),
		mk("alpha", "low", "v1"), mk("alpha", "low", "v2"),
	}

	first := metricsFor(reviews, "", "")
	if len(first.Models) != 2 {
		t.Fatalf("got %d rows, want 2", len(first.Models))
	}
	// Tied on reviews, so model name decides: alpha before zeta.
	if first.Models[0].Model != "alpha" || first.Models[1].Model != "zeta" {
		t.Errorf("order = %s, %s; want alpha before zeta on a review-count tie",
			first.Models[0].Model, first.Models[1].Model)
	}
	// Tied versions inside a row are ordered by version string, descending.
	if v := first.Models[0].Versions; len(v) != 2 || v[0].EngineVersion != "v2" || v[1].EngineVersion != "v1" {
		t.Errorf("version order = %+v, want v2 before v1 on a tie", v)
	}

	// And it must not depend on map iteration: the same input orders the same
	// way every time.
	for i := 0; i < 5; i++ {
		again := metricsFor(reviews, "", "")
		if again.Models[0].Model != first.Models[0].Model ||
			again.Models[0].Versions[0].EngineVersion != first.Models[0].Versions[0].EngineVersion {
			t.Fatalf("ordering changed between identical calls on run %d", i)
		}
	}
}

// Total and median answer different questions and must not be conflated: a
// typical review costing $3.53 says nothing about whether 400 of them are
// affordable. The column was previously labelled just "Cost" while showing a
// median, which read as a total.
func TestModelRowReportsTotalAndMedianCostSeparately(t *testing.T) {
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mk := func(version string, cost float64) store.Review {
		return store.Review{
			Model: "m", Effort: "high", EngineVersion: version, Verdict: "APPROVED",
			FreshTokens: 1, DurationSecs: 1, CostUSD: cost, ReviewedAt: at,
		}
	}
	// Costs 1, 2, 9: median 2 (upper median of the sorted three), total 12.
	got := metricsFor([]store.Review{mk("v1", 1), mk("v1", 2), mk("v2", 9)}, "", "")
	row := got.Models[0]

	if row.MedianCostUSD != 2 {
		t.Errorf("median = %v, want 2", row.MedianCostUSD)
	}
	if row.TotalCostUSD != 12 {
		t.Errorf("total = %v, want 12", row.TotalCostUSD)
	}
	// And the nested versions carry their own share of each.
	byVersion := map[string]versionMetric{}
	for _, v := range row.Versions {
		byVersion[v.EngineVersion] = v
	}
	if byVersion["v1"].TotalCostUSD != 3 || byVersion["v2"].TotalCostUSD != 9 {
		t.Errorf("version totals = %v / %v, want 3 / 9",
			byVersion["v1"].TotalCostUSD, byVersion["v2"].TotalCostUSD)
	}
	// The versions must add up to the row, or the breakdown is lying.
	if byVersion["v1"].TotalCostUSD+byVersion["v2"].TotalCostUSD != row.TotalCostUSD {
		t.Error("version totals do not sum to the row total")
	}
}

// An unpriced review adds nothing to the total and is excluded from the
// median, rather than being counted as a free review that drags it down.
func TestUnpricedReviewsDoNotDragTheMedian(t *testing.T) {
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	mk := func(cost float64) store.Review {
		return store.Review{
			Model: "m", Effort: "high", EngineVersion: "v1", Verdict: "APPROVED",
			FreshTokens: 1, DurationSecs: 1, CostUSD: cost, ReviewedAt: at,
		}
	}
	got := metricsFor([]store.Review{mk(0), mk(0), mk(4), mk(6)}, "", "")
	row := got.Models[0]

	if row.TotalCostUSD != 10 {
		t.Errorf("total = %v, want 10", row.TotalCostUSD)
	}
	// Median over the two PRICED reviews (4, 6) is 6, not 4 as it would be if
	// the two unpriced rows were folded in as zeros.
	if row.MedianCostUSD != 6 {
		t.Errorf("median = %v, want 6 over the priced reviews only", row.MedianCostUSD)
	}
	if row.Reviews != 4 {
		t.Errorf("reviews = %d, want all 4 counted", row.Reviews)
	}
}
