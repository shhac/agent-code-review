package dashboard

import (
	"context"
	"net/http"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
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

// configEngineResp is the active engine's managed dials. Which engine they
// came from is the sibling Engine field; the UI labels them with it.
type configEngineResp struct {
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
	EngineConfig     configEngineResp    `json:"engine_config"`
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
		EngineConfig:     engineConfigOf(cfg),
		Version:          s.version,
	})
}

func (s *Server) handleAuthors(w http.ResponseWriter, r *http.Request) {
	serveGet(s, w, r, func(ctx context.Context) (authorsResp, error) {
		authors, err := s.store.ListAllowedAuthors(ctx, r.URL.Query().Get("repo"))
		return authorsResp{Authors: authors}, err
	})
}

// engineConfigOf reports the managed dials of whichever engine is configured,
// so the dashboard shows what will actually run rather than always codex's.
// Empty values mean "the engine picks"; the UI renders that as a default.
func engineConfigOf(cfg config.Config) configEngineResp {
	return configEngineResp{Model: cfg.EngineModel(), Effort: cfg.EngineEffort()}
}
