package dashboard

import (
	"net/http"
	"time"

	"github.com/shhac/agent-code-review/internal/store"
)

type configRepoResp struct {
	Name               string `json:"name"`
	AllowedAuthorsOnly bool   `json:"allowed_authors_only"`
}

type configCandidateResp struct {
	NewMaxAgeDays       int    `json:"new_max_age_days"`
	RefreshedMaxAgeDays int    `json:"refreshed_max_age_days"`
	RereviewCooldown    string `json:"rereview_cooldown"`
	QuietPeriod         string `json:"quiet_period"`
}

type configScheduleResp struct {
	Enabled                 bool   `json:"enabled"`
	Interval                string `json:"interval"`
	MaxParallel             int    `json:"max_parallel"`
	UsageFloor5hPercent     int    `json:"usage_floor_5h_percent"`
	UsageFloorWeeklyPercent int    `json:"usage_floor_weekly_percent"`
}

type configDiscoveryResp struct {
	Enabled  bool   `json:"enabled"`
	Interval string `json:"interval"`
}

type configCodexResp struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

type configResp struct {
	ReviewingAs      string              `json:"reviewing_as"`
	Repos            []configRepoResp    `json:"repos"`
	Candidates       configCandidateResp `json:"candidates"`
	Schedule         configScheduleResp  `json:"schedule"`
	Discovery        configDiscoveryResp `json:"discovery"`
	ReviewRunning    bool                `json:"review_running"`
	DiscoveryRunning bool                `json:"discovery_running"`
	Engine           string              `json:"engine"`
	Codex            configCodexResp     `json:"codex"`
	Version          string              `json:"version"`
}

type authorsResp struct {
	Authors []store.AllowedAuthor `json:"authors"`
}

// handleConfig returns the operational settings the UI shows: watched repos and
// the resolved dials (with defaults applied), not the raw file.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.config()
	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()
	repos := make([]configRepoResp, 0, len(cfg.Repos))
	for _, r := range cfg.SortedRepos() {
		repos = append(repos, configRepoResp{Name: r, AllowedAuthorsOnly: cfg.AuthorScopedRepo(r)})
	}
	writeJSON(w, http.StatusOK, configResp{
		ReviewingAs: s.reviewingAs(ctx),
		Repos:       repos,
		Candidates: configCandidateResp{
			NewMaxAgeDays:       int(cfg.NewMaxAge().Hours() / 24),
			RefreshedMaxAgeDays: int(cfg.RefreshedMaxAge().Hours() / 24),
			RereviewCooldown:    cfg.RereviewCooldown().String(),
			QuietPeriod:         cfg.QuietPeriod().String(),
		},
		Schedule: configScheduleResp{
			Enabled:                 cfg.ScheduleEnabled(),
			Interval:                cfg.Interval().String(),
			MaxParallel:             cfg.MaxParallel(),
			UsageFloor5hPercent:     cfg.UsageFloor5h(),
			UsageFloorWeeklyPercent: cfg.UsageFloorWeekly(),
		},
		Discovery: configDiscoveryResp{
			Enabled:  cfg.DiscoveryEnabled(),
			Interval: cfg.DiscoverInterval().String(),
		},
		// The effective state of THIS daemon: config may say enabled while the
		// process was started with --no-schedule.
		ReviewRunning:    s.running.Review,
		DiscoveryRunning: s.running.Discovery,
		Engine:           cfg.Engine(),
		Codex: configCodexResp{
			Model:  cfg.Review.Codex.Model,
			Effort: cfg.Review.Codex.Effort,
		},
		Version: s.version,
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
	writeJSON(w, http.StatusOK, authorsResp{Authors: authors})
}
