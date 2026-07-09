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

func metricsSince(raw string, now time.Time) time.Time {
	days := map[string]int{"7d": 7, "30d": 30, "90d": 90}[raw]
	if days == 0 {
		days = 30
	}
	return now.UTC().AddDate(0, 0, -days+1).Truncate(24 * time.Hour)
}

func metricsFor(reviews []store.Review, model, effort string) metricsResp {
	resp := metricsResp{Verdicts: map[string]int{}, Activity: []metricsDay{}, Models: []modelMetric{}, Scatter: []metricsPoint{}}
	days := map[string]*metricsDay{}
	groups := map[string]*modelMetric{}
	durations := []int{}
	for _, r := range reviews {
		if model != "" && r.Model != model || effort != "" && r.Effort != effort {
			continue
		}
		resp.Summary.Reviews++
		resp.Summary.TokensUsed += r.TokensUsed
		resp.Verdicts[r.Verdict]++
		if r.DurationSecs > 0 {
			durations = append(durations, r.DurationSecs)
		}
		day := r.ReviewedAt.UTC().Format("2006-01-02")
		if days[day] == nil {
			days[day] = &metricsDay{Day: day}
		}
		days[day].Reviews++
		days[day].TokensUsed += r.TokensUsed
		key := r.Model + "\x00" + r.Effort + "\x00" + r.CodexVersion
		if groups[key] == nil {
			groups[key] = &modelMetric{Model: r.Model, Effort: r.Effort, CodexVersion: r.CodexVersion}
		}
		g := groups[key]
		g.Reviews++
		g.TokensUsed += r.TokensUsed
		resp.Scatter = append(resp.Scatter, metricsPoint{Model: r.Model, Effort: r.Effort, Verdict: r.Verdict, TokensUsed: r.TokensUsed, DurationSec: r.DurationSecs})
	}
	sort.Ints(durations)
	if len(durations) > 0 {
		resp.Summary.MedianDuration = durations[len(durations)/2]
	}
	for _, d := range days {
		resp.Activity = append(resp.Activity, *d)
	}
	sort.Slice(resp.Activity, func(i, j int) bool { return resp.Activity[i].Day < resp.Activity[j].Day })
	for _, g := range groups {
		var ds []int
		for _, r := range reviews {
			if r.Model == g.Model && r.Effort == g.Effort && r.CodexVersion == g.CodexVersion && r.DurationSecs > 0 {
				ds = append(ds, r.DurationSecs)
			}
		}
		sort.Ints(ds)
		if len(ds) > 0 {
			g.MedianDuration = ds[len(ds)/2]
		}
		resp.Models = append(resp.Models, *g)
	}
	sort.Slice(resp.Models, func(i, j int) bool { return resp.Models[i].Reviews > resp.Models[j].Reviews })
	return resp
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
