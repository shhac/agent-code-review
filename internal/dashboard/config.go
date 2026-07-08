package dashboard

import (
	"net/http"
	"time"
)

// handleConfig returns the operational settings the UI shows: watched repos and
// the resolved dials (with defaults applied), not the raw file.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.config()
	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()
	repos := make([]map[string]any, 0, len(cfg.Repos))
	for _, r := range cfg.SortedRepos() {
		repos = append(repos, map[string]any{"name": r, "allowed_authors_only": cfg.AuthorScopedRepo(r)})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"reviewing_as": s.reviewingAs(ctx),
		"repos":        repos,
		"candidates": map[string]any{
			"new_max_age_days":       int(cfg.NewMaxAge().Hours() / 24),
			"refreshed_max_age_days": int(cfg.RefreshedMaxAge().Hours() / 24),
			"rereview_cooldown":      cfg.RereviewCooldown().String(),
			"quiet_period":           cfg.QuietPeriod().String(),
		},
		"schedule": map[string]any{
			"enabled":                    cfg.Schedule.Enabled,
			"interval":                   cfg.Interval().String(),
			"max_parallel":               cfg.MaxParallel(),
			"usage_floor_5h_percent":     cfg.UsageFloor5h(),
			"usage_floor_weekly_percent": cfg.UsageFloorWeekly(),
		},
		"discovery": map[string]any{
			"enabled":  cfg.Discovery.Enabled,
			"interval": cfg.DiscoverInterval().String(),
		},
		// The effective state of THIS daemon: config may say enabled while the
		// process was started with --no-schedule.
		"review_running":    s.running.Review,
		"discovery_running": s.running.Discovery,
		"engine":            cfg.Engine(),
		"version":           s.version,
	})
}

func (s *Server) handleAuthors(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()
	authors, err := s.store.ListAllowedAuthors(ctx, r.URL.Query().Get("repo"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authors": authors})
}
