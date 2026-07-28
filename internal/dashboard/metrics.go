package dashboard

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/shhac/agent-code-review/internal/store"
)

// Token figures here are store.Review.FreshTokens throughout: what the runs
// processed, with cached re-reads left out. Raw totals are not comparable
// between engines (claude counts cached reads and they dominate a long
// session, so its totals run ~28x a codex review's), which made a single chart
// across a mixed history meaningless. The per-review total keeps its own name,
// tokens_used, and stays on the review itself — nothing here reuses that name
// for a figure ~28x smaller.
//
// A review whose fresh count is unknown (0) predates the split, and its only
// recorded figure is cache-inflated, so no aggregate here may use it. The sums
// simply add nothing for it; the scatter drops its point outright, because
// unlike a sum a point cannot represent "unknown" and would sit on the floor
// among genuinely cheap reviews. Such a review still counts as a review
// everywhere, the same way an unpriced one does below.
//
// Cost fields are an API-rate valuation, not money charged. They use
// store.Review.EffectiveCostUSD: the engine's own figure where it reported
// one, ours where it did not. Only claude reports, so without the fallback
// every codex review reads as free and the totals describe a third of the
// history. MedianCost is the number to set a per-review budget from, since a
// mean is dragged around by the long tail.
type metricsSummary struct {
	Reviews         int     `json:"reviews"`
	FreshTokens     int     `json:"fresh_tokens"`
	CacheReadTokens int     `json:"cache_read_tokens"`
	MedianDuration  int     `json:"median_duration_secs"`
	CostUSD         float64 `json:"cost_usd"`
	MedianCostUSD   float64 `json:"median_cost_usd"`
	MaxCostUSD      float64 `json:"max_cost_usd"`
	// How the cost figures above are made up. EstimatedReviews is how many of
	// PricedReviews were valued by us rather than reported by their engine, so
	// a reader can tell a measured total from a largely inferred one. A review
	// with no figure at all is in neither count.
	PricedReviews    int `json:"priced_reviews"`
	EstimatedReviews int `json:"estimated_reviews"`
	// The cross-check: across reviews whose engine reported a cost AND that we
	// also valued, what each side makes it. Divergence means our class mapping
	// or our rates are wrong, and it is the only signal that would catch that.
	CheckReportedUSD  float64 `json:"check_reported_usd"`
	CheckEstimatedUSD float64 `json:"check_estimated_usd"`
	CheckReviews      int     `json:"check_reviews"`
}

type metricsDay struct {
	Day         string `json:"day"`
	Reviews     int    `json:"reviews"`
	FreshTokens int    `json:"fresh_tokens"`
}

type modelMetric struct {
	Model         string `json:"model"`
	Effort        string `json:"effort"`
	EngineVersion string `json:"engine_version"`
	Reviews       int    `json:"reviews"`
	FreshTokens   int    `json:"fresh_tokens"`
	// CacheReadTokens is context re-read rather than processed. Reported
	// beside FreshTokens rather than as a ratio so the page can show the
	// share without the API having to pick a denominator: a row with no
	// cache read at all is a real answer, not a divide-by-zero.
	CacheReadTokens int     `json:"cache_read_tokens"`
	MedianDuration  int     `json:"median_duration_secs"`
	MedianCostUSD   float64 `json:"median_cost_usd"`
}

type metricsPoint struct {
	Model       string `json:"model"`
	Effort      string `json:"effort"`
	Verdict     string `json:"verdict"`
	FreshTokens int    `json:"fresh_tokens"`
	DurationSec int    `json:"duration_secs"`
}

type metricsResp struct {
	Summary  metricsSummary `json:"summary"`
	Verdicts map[string]int `json:"verdicts"`
	Activity []metricsDay   `json:"activity"`
	Models   []modelMetric  `json:"models"`
	Scatter  []metricsPoint `json:"scatter"`
}

type metricGroupKey struct{ model, effort, version string }
type metricGroup struct {
	metric    modelMetric
	durations []int
	costs     []float64
}

func matchesMetricsFilter(r store.Review, model, effort string) bool {
	return (model == "" || r.Model == model) && (effort == "" || r.Effort == effort)
}

func medianDuration(durations []int) int {
	if len(durations) == 0 {
		return 0
	}
	sort.Ints(durations)
	return durations[len(durations)/2]
}

func medianCost(costs []float64) float64 {
	if len(costs) == 0 {
		return 0
	}
	sort.Float64s(costs)
	return costs[len(costs)/2]
}

func metricsSince(raw string, now time.Time) time.Time {
	days := map[string]int{"7d": 7, "30d": 30, "90d": 90}[raw]
	if days == 0 {
		days = 30
	}
	return now.UTC().AddDate(0, 0, -days+1).Truncate(24 * time.Hour)
}

// metricsFor filters once, then computes each aggregate in its own pure
// function: a new metric is a new function plus a resp field, not an edit
// inside a shared fold. The extra passes are negligible (bounded 90-day
// review list).
func metricsFor(reviews []store.Review, model, effort string) metricsResp {
	filtered := make([]store.Review, 0, len(reviews))
	for _, r := range reviews {
		if matchesMetricsFilter(r, model, effort) {
			filtered = append(filtered, r)
		}
	}
	return metricsResp{
		Summary:  summaryOf(filtered),
		Verdicts: verdictCounts(filtered),
		Activity: activityByDay(filtered),
		Models:   modelGroups(filtered),
		Scatter:  scatterPoints(filtered),
	}
}

func summaryOf(reviews []store.Review) metricsSummary {
	s := metricsSummary{Reviews: len(reviews)}
	durations := []int{}
	costs := []float64{}
	for _, r := range reviews {
		s.FreshTokens += r.FreshTokens
		s.CacheReadTokens += r.CacheReadTokens
		cost := r.EffectiveCostUSD()
		s.CostUSD += cost
		if r.DurationSecs > 0 {
			durations = append(durations, r.DurationSecs)
		}
		// Only reviews with a spend figure shape the median and max. A review
		// with none is unknown rather than free, and folding its 0 in would
		// halve the median of a mixed history and make a budget derived from
		// it far too tight.
		if cost > 0 {
			costs = append(costs, cost)
			s.MaxCostUSD = max(s.MaxCostUSD, cost)
			s.PricedReviews++
			if r.CostEstimated() {
				s.EstimatedReviews++
			}
		}
		// Both figures on one review: the only place our rates can be checked
		// against an engine that prices its own runs.
		if r.CostUSD > 0 && r.EstCostUSD > 0 {
			s.CheckReviews++
			s.CheckReportedUSD += r.CostUSD
			s.CheckEstimatedUSD += r.EstCostUSD
		}
	}
	s.MedianDuration = medianDuration(durations)
	s.MedianCostUSD = medianCost(costs)
	return s
}

func verdictCounts(reviews []store.Review) map[string]int {
	counts := map[string]int{}
	for _, r := range reviews {
		counts[r.Verdict]++
	}
	return counts
}

func activityByDay(reviews []store.Review) []metricsDay {
	days := map[string]*metricsDay{}
	for _, r := range reviews {
		day := r.ReviewedAt.UTC().Format("2006-01-02")
		if days[day] == nil {
			days[day] = &metricsDay{Day: day}
		}
		days[day].Reviews++
		days[day].FreshTokens += r.FreshTokens
	}
	activity := make([]metricsDay, 0, len(days))
	for _, d := range days {
		activity = append(activity, *d)
	}
	sort.Slice(activity, func(i, j int) bool { return activity[i].Day < activity[j].Day })
	return activity
}

func modelGroups(reviews []store.Review) []modelMetric {
	groups := map[metricGroupKey]*metricGroup{}
	for _, r := range reviews {
		key := metricGroupKey{r.Model, r.Effort, r.EngineVersion}
		if groups[key] == nil {
			groups[key] = &metricGroup{metric: modelMetric{Model: r.Model, Effort: r.Effort, EngineVersion: r.EngineVersion}}
		}
		g := groups[key]
		g.metric.Reviews++
		g.metric.FreshTokens += r.FreshTokens
		g.metric.CacheReadTokens += r.CacheReadTokens
		if r.DurationSecs > 0 {
			g.durations = append(g.durations, r.DurationSecs)
		}
		if cost := r.EffectiveCostUSD(); cost > 0 {
			g.costs = append(g.costs, cost)
		}
	}
	models := make([]modelMetric, 0, len(groups))
	for _, g := range groups {
		g.metric.MedianDuration = medianDuration(g.durations)
		g.metric.MedianCostUSD = medianCost(g.costs)
		models = append(models, g.metric)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Reviews > models[j].Reviews })
	return models
}

func scatterPoints(reviews []store.Review) []metricsPoint {
	points := make([]metricsPoint, 0, len(reviews))
	for _, r := range reviews {
		// A point needs both axes to sit anywhere honest; an unknown token
		// count would plot on the floor next to genuinely cheap reviews.
		if r.FreshTokens == 0 {
			continue
		}
		points = append(points, metricsPoint{Model: r.Model, Effort: r.Effort, Verdict: r.Verdict, FreshTokens: r.FreshTokens, DurationSec: r.DurationSecs})
	}
	return points
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	serveGet(s, w, r, func(ctx context.Context) (metricsResp, error) {
		reviews, err := s.store.ListReviewsSince(ctx, metricsSince(r.URL.Query().Get("range"), time.Now()))
		if err != nil {
			return metricsResp{}, err
		}
		return metricsFor(reviews, r.URL.Query().Get("model"), r.URL.Query().Get("effort")), nil
	})
}
