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
	// Reviews is real verdicts: work the engine actually did. Outcomes is
	// every recorded row including precheck skips and errors. They are
	// reported apart because the difference is large and load-bearing, and one
	// number labelled "reviews" covering both is how three pages ended up
	// disagreeing about the same word.
	Reviews         int     `json:"reviews"`
	Outcomes        int     `json:"outcomes"`
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
	Model  string `json:"model"`
	Effort string `json:"effort"`
	// Embedded and deliberately UNTAGGED: an untagged, non-pointer embedded
	// struct inlines its fields into the same JSON object, so the wire shape is
	// unchanged. A tag here, or a pointer, would nest the figures under a key
	// and break the page silently.
	usageMetric
	// Versions breaks the same figures down by the CLI that produced them.
	//
	// Nested rather than another dimension on the key, because the question
	// "what does this model cost me" is asked far more often than "what did
	// v0.31.2 of the CLI cost me", and keying on the version split every model
	// into a row per release nobody was comparing.
	//
	// The medians above are computed over ALL of this model's reviews, not
	// averaged from the per-version medians below: a median of medians is not
	// a median, and weighting it by review count would present an estimate as
	// a measurement.
	Versions []versionMetric `json:"versions"`
}

// usageMetric is the figure set BOTH levels of the breakdown report. One
// struct rather than two identical field lists, so adding a figure is a single
// edit instead of four (two structs, two accumulation sites), with a silent
// row of zeros waiting if you forget one.
//
// CacheReadTokens is context re-read rather than processed. Reported beside
// FreshTokens rather than as a ratio so the page can show the share without
// the API having to pick a denominator: a row with no cache read at all is a
// real answer, not a divide-by-zero.
type usageMetric struct {
	Reviews         int     `json:"reviews"`
	FreshTokens     int     `json:"fresh_tokens"`
	CacheReadTokens int     `json:"cache_read_tokens"`
	MedianDuration  int     `json:"median_duration_secs"`
	MedianCostUSD   float64 `json:"median_cost_usd"`
	// TotalCostUSD is what this model or version has cost in aggregate, which
	// is the question a median cannot answer: a typical review costing $3.53
	// says nothing about whether 400 of them are worth it. Summed the same way
	// as the page's headline total (EffectiveCostUSD, so a codex review
	// contributes our own valuation rather than nothing), which is why the two
	// agree when you add the rows up.
	TotalCostUSD float64 `json:"total_cost_usd"`
}

// versionMetric is one CLI version's share of a model+effort row.
type versionMetric struct {
	EngineVersion string `json:"engine_version"`
	usageMetric
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

// metricGroupKey identifies one model + effort. The CLI version is
// deliberately NOT part of it: keying on the version split every model into a
// row per release, which nobody was comparing. It is a nested breakdown now.
type metricGroupKey struct{ Model, Effort string }

// metricAcc accumulates one level of the breakdown. The durations and costs
// are kept rather than folded so the median can be a real one, and each level
// keeps its OWN so neither is ever a median of medians.
type metricAcc struct {
	usage     usageMetric
	durations []int
	costs     []float64
}

// add folds one review in. Written once and called at both levels, so the two
// cannot come to disagree about what counts.
func (a *metricAcc) add(r store.Review) {
	a.usage.Reviews++
	a.usage.FreshTokens += r.FreshTokens
	a.usage.CacheReadTokens += r.CacheReadTokens
	if r.DurationSecs > 0 {
		a.durations = append(a.durations, r.DurationSecs)
	}
	// The total takes every review's figure; the median takes only the priced
	// ones. A review with no figure must not drag the median toward zero, but
	// it genuinely adds nothing to the total either, so both are correct.
	cost := r.EffectiveCostUSD()
	a.usage.TotalCostUSD += cost
	if cost > 0 {
		a.costs = append(a.costs, cost)
	}
}

// finish computes the medians over everything this level accumulated.
func (a *metricAcc) finish() usageMetric {
	a.usage.MedianDuration = medianDuration(a.durations)
	a.usage.MedianCostUSD = medianCost(a.costs)
	return a.usage
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
	// Every aggregate about WORK DONE counts real verdicts only. A precheck
	// skip spends no tokens and takes a second, so counting it as a review
	// inflated "reviews completed" and dragged every median toward zero — and
	// in this store skips have been up to half of all recorded rows. The
	// verdict breakdown is the exception: skips and errors are exactly what it
	// exists to show.
	real := make([]store.Review, 0, len(filtered))
	for _, r := range filtered {
		if store.IsRealVerdict(r.Verdict) {
			real = append(real, r)
		}
	}
	return metricsResp{
		Summary:  summaryOf(real, len(filtered)),
		Verdicts: verdictCounts(filtered),
		Activity: activityByDay(real),
		Models:   modelGroups(real),
		Scatter:  scatterPoints(real),
	}
}

func summaryOf(reviews []store.Review, outcomes int) metricsSummary {
	s := metricsSummary{Reviews: len(reviews), Outcomes: outcomes}
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

// modelGroups summarises reviews by model + effort, with each CLI version's
// share nested inside.
//
// Both levels accumulate their own durations and costs so both medians are
// real. Deriving the outer one from the inner ones would be a median of
// medians, which is not a median.
func modelGroups(reviews []store.Review) []modelMetric {
	type group struct {
		model, effort string
		acc           metricAcc
		versions      map[string]*metricAcc
	}
	groups := map[metricGroupKey]*group{}
	for _, r := range reviews {
		key := metricGroupKey{Model: r.Model, Effort: r.Effort}
		if groups[key] == nil {
			groups[key] = &group{model: r.Model, effort: r.Effort, versions: map[string]*metricAcc{}}
		}
		g := groups[key]
		g.acc.add(r)
		if g.versions[r.EngineVersion] == nil {
			g.versions[r.EngineVersion] = &metricAcc{}
		}
		g.versions[r.EngineVersion].add(r)
	}

	models := make([]modelMetric, 0, len(groups))
	for _, g := range groups {
		m := modelMetric{Model: g.model, Effort: g.effort, usageMetric: g.acc.finish()}
		m.Versions = make([]versionMetric, 0, len(g.versions))
		for version, acc := range g.versions {
			m.Versions = append(m.Versions, versionMetric{EngineVersion: version, usageMetric: acc.finish()})
		}
		// Busiest version first, with the version string as a stable tiebreak so
		// equal counts do not reshuffle between requests.
		sort.Slice(m.Versions, func(i, j int) bool {
			if m.Versions[i].Reviews != m.Versions[j].Reviews {
				return m.Versions[i].Reviews > m.Versions[j].Reviews
			}
			return m.Versions[i].EngineVersion > m.Versions[j].EngineVersion
		})
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Reviews != models[j].Reviews {
			return models[i].Reviews > models[j].Reviews
		}
		if models[i].Model != models[j].Model {
			return models[i].Model < models[j].Model
		}
		return models[i].Effort < models[j].Effort
	})
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
