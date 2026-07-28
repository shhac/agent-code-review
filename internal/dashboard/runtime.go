package dashboard

import (
	"net/http"
	"time"

	"github.com/shhac/agent-code-review/internal/logbuf"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/usage"
)

// engineUsage is one engine's meter as the dashboard renders it. Available
// distinguishes "we have numbers" from "we tried and failed", which the
// snapshot alone cannot: a failed poll still stamps FetchedAt. Error carries
// the reason so an unavailable engine explains itself instead of showing a
// blank meter.
type engineUsage struct {
	Engine    string          `json:"engine"`
	Active    bool            `json:"active"`
	Available bool            `json:"available"`
	Error     string          `json:"error,omitempty"`
	Usage     *usage.Snapshot `json:"usage,omitempty"`
}

type usageResp struct {
	// Available reports whether the ACTIVE engine has usable numbers, kept
	// for the panel's overall state; per-engine detail is in Engines.
	Available bool `json:"available"`
	// Engine is the configured engine, so the UI knows which slot to open on.
	Engine string `json:"engine"`
	// Engines carries every metered engine, so an operator can see the one
	// they are NOT using has headroom before deciding to switch.
	Engines      []engineUsage `json:"engines"`
	ReviewPaused bool          `json:"review_paused,omitempty"`
	PausedReason string        `json:"paused_reason,omitempty"`
	TokensTotal  int64         `json:"fresh_tokens_total"`
	Tokens24h    int64         `json:"fresh_tokens_24h"`
}

// engineUsages renders every engine's cached snapshot in a stable order, with
// the configured one marked. Engines that were never polled still appear, as
// unavailable: a missing slot would read as "this engine does not exist".
func engineUsages(snaps map[string]usage.Snapshot, active string) []engineUsage {
	out := make([]engineUsage, 0, len(review.Engines))
	for _, engine := range review.Engines {
		snap := snaps[engine]
		row := engineUsage{Engine: engine, Active: engine == active, Available: snap.OK(), Error: snap.Error}
		if !snap.FetchedAt.IsZero() {
			row.Usage = &snap
		}
		if !row.Available && row.Error == "" {
			row.Error = "no usage reported yet"
		}
		out = append(out, row)
	}
	return out
}

type logsResp struct {
	Available bool           `json:"available"`
	Entries   []logbuf.Entry `json:"entries"`
}

type healthResp struct {
	Status string `json:"status"`
}

// handleUsage returns the cached rate-limit snapshot for the configured
// review engine (refreshed by the
// daemon on dashboard.usage_poll_interval) plus the usage-floor verdict the
// scheduler applies to it, so the UI can show why reviews are paused. It
// also carries the history's token-spend sums (all time and the last 24h);
// unlike the rate-limit windows those come from the store, so they're
// present even when the daemon isn't polling usage.
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()
	tokensTotal, err := s.store.FreshTokens(ctx, time.Time{})
	if err != nil {
		s.fail(w, err)
		return
	}
	tokens24h, err := s.store.FreshTokens(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		s.fail(w, err)
		return
	}
	if s.usage == nil {
		writeJSON(w, http.StatusOK, usageResp{
			Available: false, Engine: s.config().Engine(),
			Engines:     engineUsages(nil, s.config().Engine()),
			TokensTotal: tokensTotal, Tokens24h: tokens24h,
		})
		return
	}
	cfg := s.config()
	active := cfg.Engine()
	snaps := s.usage.All()
	// The floor applies to the ACTIVE engine only: that is the account
	// reviews spend from, so another engine's headroom must not pause or
	// unpause the loop.
	snap := snaps[active]
	paused, reason := usage.BelowFloor(snap, cfg.UsageFloor5h(), cfg.UsageFloorWeekly())
	writeJSON(w, http.StatusOK, usageResp{
		Available:    snap.OK(),
		Engine:       active,
		Engines:      engineUsages(snaps, active),
		ReviewPaused: paused,
		PausedReason: reason,
		TokensTotal:  tokensTotal,
		Tokens24h:    tokens24h,
	})
}

// handleLogs returns the newest captured daemon log lines, oldest first.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.logs == nil {
		writeJSON(w, http.StatusOK, logsResp{Available: false, Entries: []logbuf.Entry{}})
		return
	}
	writeJSON(w, http.StatusOK, logsResp{Available: true, Entries: s.logs.Tail(queryInt(r, "n", 500, 1000))})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResp{Status: "ok"})
}
