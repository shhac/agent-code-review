package dashboard

import (
	"net/http"
	"sort"
	"time"

	"github.com/shhac/agent-code-review/internal/store"
)

type metricsSummary struct {
	Reviews        int `json:"reviews"`
	TokensUsed     int `json:"tokens_used"`
	MedianDuration int `json:"median_duration_secs"`
}

type metricsDay struct {
	Day        string `json:"day"`
	Reviews    int    `json:"reviews"`
	TokensUsed int    `json:"tokens_used"`
}

type modelMetric struct {
	Model          string `json:"model"`
	Effort         string `json:"effort"`
	CodexVersion   string `json:"codex_version"`
	Reviews        int    `json:"reviews"`
	TokensUsed     int    `json:"tokens_used"`
	MedianDuration int    `json:"median_duration_secs"`
}

type metricsPoint struct {
	Model       string `json:"model"`
	Effort      string `json:"effort"`
	Verdict     string `json:"verdict"`
	TokensUsed  int    `json:"tokens_used"`
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
	for _, r := range reviews {
		s.TokensUsed += r.TokensUsed
		if r.DurationSecs > 0 {
			durations = append(durations, r.DurationSecs)
		}
	}
	s.MedianDuration = medianDuration(durations)
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
		days[day].TokensUsed += r.TokensUsed
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
		key := metricGroupKey{r.Model, r.Effort, r.CodexVersion}
		if groups[key] == nil {
			groups[key] = &metricGroup{metric: modelMetric{Model: r.Model, Effort: r.Effort, CodexVersion: r.CodexVersion}}
		}
		g := groups[key]
		g.metric.Reviews++
		g.metric.TokensUsed += r.TokensUsed
		if r.DurationSecs > 0 {
			g.durations = append(g.durations, r.DurationSecs)
		}
	}
	models := make([]modelMetric, 0, len(groups))
	for _, g := range groups {
		g.metric.MedianDuration = medianDuration(g.durations)
		models = append(models, g.metric)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Reviews > models[j].Reviews })
	return models
}

func scatterPoints(reviews []store.Review) []metricsPoint {
	points := make([]metricsPoint, 0, len(reviews))
	for _, r := range reviews {
		points = append(points, metricsPoint{Model: r.Model, Effort: r.Effort, Verdict: r.Verdict, TokensUsed: r.TokensUsed, DurationSec: r.DurationSecs})
	}
	return points
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()
	reviews, err := s.store.ListReviewsSince(ctx, metricsSince(r.URL.Query().Get("range"), time.Now()))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, metricsFor(reviews, r.URL.Query().Get("model"), r.URL.Query().Get("effort")))
}
