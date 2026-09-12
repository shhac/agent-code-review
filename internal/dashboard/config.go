package dashboard

import (
	"context"
	"net/http"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/score"
	"github.com/shhac/agent-code-review/internal/store"
)

// configScoringResp is the global scoring switch, as the UI needs it.
//
// Mode and LeaderboardVisible are both present because they answer different
// questions: mode is what the operator set, visibility is what the page should
// do about it. Turning off the measuring deliberately does NOT hide the points
// already earned, so a UI deriving one from the other would get it wrong.
type configScoringResp struct {
	Mode               string `json:"mode"`
	LeaderboardVisible bool   `json:"leaderboard_visible"`
	// The dials behind a score, so "why did this PR earn that" is answerable
	// from the page rather than only from `config show`. Resolved values, not
	// the raw document: what a review is actually scored under is what an
	// operator needs, and the per-repo overrides mean the file alone does not
	// say it.
	PieceLines          float64 `json:"piece_lines"`
	SizePoints          float64 `json:"size_points"`
	SizeFalloff         float64 `json:"size_falloff"`
	RemovalPointsPer100 float64 `json:"removal_points_per_100"`
	Approved            float64 `json:"approved"`
	Commented           float64 `json:"commented"`
	RequestedChanges    float64 `json:"requested_changes"`
	AttemptDecay        float64 `json:"attempt_decay"`
	UseGitattributes    bool    `json:"use_gitattributes"`
	ExcludePaths        int     `json:"exclude_paths"`
	// Peak is the changed-line count that earns the most as a single PR. It is
	// DERIVED from piece_lines and size_falloff rather than set, and it is the
	// number people actually want ("how big should this PR be"), so the page
	// should not have to work it out and get it subtly wrong.
	Peak float64 `json:"peak"`
	// Tiers are the labels a score is explained with, and where they fall.
	// They follow the dials rather than being configured, so sending them
	// keeps the page from carrying its own copy of the boundaries.
	Tiers []score.Tier `json:"tiers"`
	// ScopedRepos are the repos NOT described by the figures above: they
	// narrow the policy with their own. Sent so the page can say so rather
	// than presenting the global policy as everybody's.
	ScopedRepos []string `json:"scoped_repos"`
}

// scoringResp resolves the scoring dials as reviews actually see them.
func scoringResp(cfg config.Config) configScoringResp {
	r := cfg.ResolveScoring("")
	return configScoringResp{
		Mode:                cfg.ScoringMode(""),
		LeaderboardVisible:  cfg.LeaderboardVisible(""),
		PieceLines:          r.PieceLines,
		SizePoints:          r.SizePoints,
		SizeFalloff:         r.SizeFalloff,
		RemovalPointsPer100: r.RemovalPointsPer100,
		Approved:            r.Approved,
		Commented:           r.Commented,
		RequestedChanges:    r.RequestedChanges,
		AttemptDecay:        r.AttemptDecay,
		Peak:                r.Peak(),
		Tiers:               r.Tiers(),
		ScopedRepos:         cfg.ScoringScopedRepos(),
		UseGitattributes:    r.UseGitattributes,
		ExcludePaths:        len(r.ExcludePaths),
	}
}

type configRepoResp struct {
	Name string `json:"name"`
	// AllowedAuthorsOnly predates groups and is kept for the UI: it now means
	// "an author with no roster row is not discovered here", which is what it
	// always meant, just derived from the unlisted policy instead of a repo
	// list. UnlistedGroup names the group that decided it.
	AllowedAuthorsOnly bool   `json:"allowed_authors_only"`
	UnlistedGroup      string `json:"unlisted_group"`
}

type configCandidateResp struct {
	NewMaxAgeDays        int    `json:"new_max_age_days"`
	RefreshedMaxAgeDays  int    `json:"refreshed_max_age_days"`
	DiscussionMaxAgeDays int    `json:"discussion_max_age_days"`
	RereviewCooldown     string `json:"rereview_cooldown"`
	QuietPeriod          string `json:"quiet_period"`
	SteeringHold         string `json:"steering_hold"`
	ErrorBackoff         string `json:"error_backoff"`
}

type configScheduleResp struct {
	Enabled                 bool   `json:"enabled"`
	Interval                string `json:"interval"`
	MaxParallel             int    `json:"max_parallel"`
	DispatchCooldown        string `json:"dispatch_cooldown"`
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
	// WorkspaceRetention is how long a finished review's transcript is kept.
	// Surfaced because nothing else says it, and a transcript that has aged
	// out is the difference between a postmortem and a shrug.
	WorkspaceRetention string            `json:"workspace_retention"`
	Scoring            configScoringResp `json:"scoring"`
}

// authorRow is one roster entry with the policy it actually resolves to. The
// resolution is included rather than left to the reader because a row can name
// a group that config no longer defines, and the resolved view is the only
// place that shows.
type authorRow struct {
	store.Author
	Policy config.Policy `json:"policy"`
}

type authorsResp struct {
	Authors []authorRow `json:"authors"`
}

// handleConfig returns the operational settings the UI shows: watched repos and
// the resolved dials (with defaults applied), not the raw file.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.config()
	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()
	repos := make([]configRepoResp, 0, len(cfg.Repos))
	for _, r := range cfg.SortedRepos() {
		unlisted := cfg.UnlistedPolicy(r)
		repos = append(repos, configRepoResp{
			Name:               r,
			AllowedAuthorsOnly: !unlisted.Reviewable(),
			UnlistedGroup:      unlisted.Group,
		})
	}
	writeJSON(w, http.StatusOK, configResp{
		ReviewingAs: s.reviewingAs(ctx),
		Repos:       repos,
		Candidates: configCandidateResp{
			NewMaxAgeDays:        int(cfg.NewMaxAge().Hours() / 24),
			RefreshedMaxAgeDays:  int(cfg.RefreshedMaxAge().Hours() / 24),
			DiscussionMaxAgeDays: int(cfg.DiscussionMaxAge().Hours() / 24),
			RereviewCooldown:     cfg.RereviewCooldown().String(),
			QuietPeriod:          cfg.QuietPeriod().String(),
			SteeringHold:         cfg.SteeringHold().String(),
			ErrorBackoff:         cfg.ErrorBackoff().String(),
		},
		Scoring: scoringResp(cfg),
		Schedule: configScheduleResp{
			Enabled:                 cfg.ScheduleEnabled(),
			Interval:                cfg.Interval().String(),
			MaxParallel:             cfg.MaxParallel(),
			DispatchCooldown:        cfg.DispatchCooldown().String(),
			UsageFloor5hPercent:     cfg.UsageFloor5h(),
			UsageFloorWeeklyPercent: cfg.UsageFloorWeekly(),
		},
		Discovery: configDiscoveryResp{
			Enabled:  cfg.DiscoveryEnabled(),
			Interval: cfg.DiscoverInterval().String(),
		},
		// The effective state of THIS daemon: config may say enabled while the
		// process was started with --no-schedule.
		ReviewRunning:      s.running.Review,
		DiscoveryRunning:   s.running.Discovery,
		Engine:             cfg.Engine(),
		EngineConfig:       engineConfigOf(cfg),
		Version:            s.version,
		WorkspaceRetention: cfg.WorkspaceRetention().String(),
	})
}

func (s *Server) handleAuthors(w http.ResponseWriter, r *http.Request) {
	serveGet(s, w, r, func(ctx context.Context) (authorsResp, error) {
		q := r.URL.Query()
		authors, err := s.store.ListAuthors(ctx, q.Get("repo"), q.Get("group"))
		if err != nil {
			return authorsResp{}, err
		}
		cfg := s.config()
		rows := make([]authorRow, 0, len(authors))
		for _, a := range authors {
			rows = append(rows, authorRow{
				Author: a,
				Policy: cfg.ResolvePolicy(a.Repo, a.GitHubHandle, a.Membership()),
			})
		}
		return authorsResp{Authors: rows}, nil
	})
}

// engineConfigOf reports the managed dials of whichever engine is configured,
// so the dashboard shows what will actually run rather than always codex's.
// Empty values mean "the engine picks"; the UI renders that as a default.
func engineConfigOf(cfg config.Config) configEngineResp {
	return configEngineResp{Model: cfg.EngineModel(), Effort: cfg.EngineEffort()}
}
