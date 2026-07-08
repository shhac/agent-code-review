package dashboard

import (
	"net/http"
	"time"

	"github.com/shhac/agent-code-review/internal/logbuf"
	"github.com/shhac/agent-code-review/internal/usage"
)

// handleUsage returns the cached Codex rate-limit snapshot (refreshed by the
// daemon on dashboard.usage_poll_interval) plus the usage-floor verdict the
// scheduler applies to it, so the UI can show why reviews are paused. It
// also carries the history's token-spend sums (all time and the last 24h);
// unlike the rate-limit windows those come from the store, so they're
// present even when the daemon isn't polling usage.
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()
	tokensTotal, err := s.store.TokensUsed(ctx, time.Time{})
	if err != nil {
		s.fail(w, err)
		return
	}
	tokens24h, err := s.store.TokensUsed(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		s.fail(w, err)
		return
	}
	if s.usage == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "tokens_total": tokensTotal, "tokens_24h": tokens24h})
		return
	}
	snap := s.usage.Get()
	cfg := s.config()
	paused, reason := usage.BelowFloor(snap, cfg.UsageFloor5h(), cfg.UsageFloorWeekly())
	writeJSON(w, http.StatusOK, map[string]any{
		"available":     !snap.FetchedAt.IsZero(),
		"usage":         snap,
		"review_paused": paused,
		"paused_reason": reason,
		"tokens_total":  tokensTotal,
		"tokens_24h":    tokens24h,
	})
}

// handleLogs returns the newest captured daemon log lines, oldest first.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.logs == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "entries": []logbuf.Entry{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "entries": s.logs.Tail(queryInt(r, "n", 500, 1000))})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
