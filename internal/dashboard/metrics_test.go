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

func TestMetricsForBucketsDaysAndSortsGroups(t *testing.T) {
	west := time.FixedZone("west", -5*3600)
	reviews := []store.Review{
		// group A (gpt-5.5/high/v1): 2 reviews on 07-08.
		{Model: "gpt-5.5", Effort: "high", CodexVersion: "v1", Verdict: "APPROVED", TokensUsed: 100, DurationSecs: 10, ReviewedAt: time.Date(2026, 7, 8, 9, 0, 0, 0, time.UTC)},
		{Model: "gpt-5.5", Effort: "high", CodexVersion: "v1", Verdict: "COMMENTED", TokensUsed: 100, DurationSecs: 30, ReviewedAt: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)},
		// group B (gpt-5.6/medium/v2): 1 review whose western-evening local time rolls into 07-09 UTC.
		{Model: "gpt-5.6", Effort: "medium", CodexVersion: "v2", Verdict: "APPROVED", TokensUsed: 50, DurationSecs: 20, ReviewedAt: time.Date(2026, 7, 8, 22, 0, 0, 0, west)},
		// group C (gpt-6/low/v3): 3 reviews on 07-10 — the largest group.
		{Model: "gpt-6", Effort: "low", CodexVersion: "v3", Verdict: "APPROVED", TokensUsed: 10, DurationSecs: 5, ReviewedAt: time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)},
		{Model: "gpt-6", Effort: "low", CodexVersion: "v3", Verdict: "SKIPPED", TokensUsed: 10, DurationSecs: 5, ReviewedAt: time.Date(2026, 7, 10, 2, 0, 0, 0, time.UTC)},
		{Model: "gpt-6", Effort: "low", CodexVersion: "v3", Verdict: "APPROVED", TokensUsed: 10, DurationSecs: 5, ReviewedAt: time.Date(2026, 7, 10, 3, 0, 0, 0, time.UTC)},
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
		if got.Summary.Reviews != 0 || got.Summary.TokensUsed != 0 || got.Summary.MedianDuration != 0 {
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
