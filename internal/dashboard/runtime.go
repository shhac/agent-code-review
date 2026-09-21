package dashboard

import (
	"net/http"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/logbuf"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/usage"
)

// engineUsage is one engine's meter as the dashboard renders it. Available
// distinguishes "we have numbers" from "we tried and failed", which the
// snapshot alone cannot: a failed poll still stamps FetchedAt. Error carries
// the reason so an unavailable engine explains itself instead of showing a
// blank meter.
//
// Paused is judged against this engine's BASE floor. A cohort that overrides
// the floor can be held while the panel shows the engine as fine; that hold
// explains itself on the candidate, where it applies, rather than being
// averaged into a panel-wide verdict that is true for nobody.
type engineUsage struct {
	Engine       string          `json:"engine"`
	Active       bool            `json:"active"`
	Available    bool            `json:"available"`
	Error        string          `json:"error,omitempty"`
	Usage        *usage.Snapshot `json:"usage,omitempty"`
	Paused       bool            `json:"paused,omitempty"`
	PausedReason string          `json:"paused_reason,omitempty"`
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
	FreshTotal   int64         `json:"fresh_tokens_total"`
	Fresh24h     int64         `json:"fresh_tokens_24h"`
}

// engineUsages renders every engine's cached snapshot in a stable order, with
// the configured one marked and each judged against its OWN floor. Engines
// that were never polled still appear, as unavailable: a missing slot would
// read as "this engine does not exist".
func engineUsages(cfg config.Config, snaps map[string]usage.Snapshot, active string) []engineUsage {
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
		fiveH, oneW := cfg.Review.UsageFloors(engine)
		row.Paused, row.PausedReason = usage.BelowFloor(snap, fiveH, oneW)
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
	freshTotal, err := s.store.FreshTokens(ctx, time.Time{})
	if err != nil {
		s.fail(w, err)
		return
	}
	fresh24h, err := s.store.FreshTokens(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		s.fail(w, err)
		return
	}
	var snaps map[string]usage.Snapshot
	if s.usage != nil {
		snaps = s.usage.All()
	}
	writeJSON(w, http.StatusOK, usageView(s.config(), snaps, freshTotal, fresh24h))
}

// usageView shapes the usage response. Pure, so the floor and availability
// rules table-test without an HTTP round trip or a poller; the handler keeps
// only the two store reads and the transport. A nil snapshot map is the
// no-poller case (one-shot runs, --read-only), which reports unavailable
// rather than pretending to headroom it never measured.
func usageView(cfg config.Config, snaps map[string]usage.Snapshot, freshTotal, fresh24h int64) usageResp {
	active := cfg.Engine()
	resp := usageResp{
		Engine:     active,
		Engines:    engineUsages(cfg, snaps, active),
		FreshTotal: freshTotal,
		Fresh24h:   fresh24h,
	}
	if snaps == nil {
		return resp
	}
	// The top-level verdict is the ACTIVE engine's, read back off its row
	// rather than recomputed: that is the account reviews spend from by
	// default, and it is what the page header reports. Every engine's own
	// verdict travels on its row.
	resp.Available = snaps[active].OK()
	for _, row := range resp.Engines {
		if row.Active {
			resp.ReviewPaused, resp.PausedReason = row.Paused, row.PausedReason
		}
	}
	return resp
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
