package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
)

type leaderboardStore struct {
	dashboardStore // panic on anything not overridden
	board          []store.AuthorScore
	authors        []store.Author
	gotQuery       store.LeaderboardQuery
}

func (f *leaderboardStore) Leaderboard(_ context.Context, q store.LeaderboardQuery) ([]store.AuthorScore, error) {
	f.gotQuery = q
	return f.board, nil
}

func (f *leaderboardStore) ListAuthors(_ context.Context, _, _ string) ([]store.Author, error) {
	return f.authors, nil
}

func getLeaderboard(t *testing.T, fs *leaderboardStore, cfg config.Config, url string) leaderboardResp {
	t.Helper()
	s := NewServer(Deps{Store: fs, Config: func() config.Config { return cfg }})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got leaderboardResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

func TestLeaderboardRanksAndNames(t *testing.T) {
	fs := &leaderboardStore{
		board: []store.AuthorScore{
			{Author: "alice", Total: 248, Reviews: 3, Approvals: 2, Additions: 80, Deletions: 820},
			{Author: "bob", Total: 100, Reviews: 1, Approvals: 1},
		},
		authors: []store.Author{{GitHubHandle: "alice", Name: "Alice Example"}},
	}
	got := getLeaderboard(t, fs, config.Config{}, "/api/leaderboard")

	if len(got.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(got.Entries))
	}
	if got.Entries[0].Rank != 1 || got.Entries[0].Author != "alice" || got.Entries[0].Total != 248 {
		t.Errorf("first = %+v", got.Entries[0])
	}
	if got.Entries[0].Name != "Alice Example" {
		t.Errorf("name = %q, want the roster's display name", got.Entries[0].Name)
	}
	// An author with PRs but no roster row must still appear: that is exactly
	// what authors.unlisted exists for.
	if got.Entries[1].Rank != 2 || got.Entries[1].Author != "bob" {
		t.Errorf("second = %+v", got.Entries[1])
	}
	if got.Entries[1].Name != "" {
		t.Errorf("name = %q, want empty for an author with no roster row", got.Entries[1].Name)
	}
}

// days=0 means all of history. A board that silently covered one day would
// flatter whoever shipped yesterday.
func TestLeaderboardDefaultsToAllHistory(t *testing.T) {
	fs := &leaderboardStore{}
	getLeaderboard(t, fs, config.Config{}, "/api/leaderboard")
	if !fs.gotQuery.Since.IsZero() {
		t.Errorf("Since = %v, want the zero time (all history)", fs.gotQuery.Since)
	}
}

func TestLeaderboardNarrowing(t *testing.T) {
	fs := &leaderboardStore{}
	got := getLeaderboard(t, fs, config.Config{}, "/api/leaderboard?days=30&repo=o/r")
	if fs.gotQuery.Since.IsZero() {
		t.Error("days=30 should set a Since bound")
	}
	if fs.gotQuery.Repo != "o/r" {
		t.Errorf("Repo = %q, want o/r", fs.gotQuery.Repo)
	}
	if got.Days != 30 || got.Repo != "o/r" {
		t.Errorf("the response should echo the window it covers, got %+v", got)
	}
}

// The page has to tell "nobody has earned anything" from "scoring is off", or
// an empty board reads as the former when it is the latter.
func TestLeaderboardReportsWhenScoringIsDisabled(t *testing.T) {
	off := false
	cfg := config.Config{Scoring: config.ScoringSettings{Enabled: &off}}
	got := getLeaderboard(t, &leaderboardStore{}, cfg, "/api/leaderboard")
	if got.Enabled {
		t.Error("Enabled should be false when scoring is switched off")
	}

	got = getLeaderboard(t, &leaderboardStore{}, config.Config{}, "/api/leaderboard")
	if !got.Enabled {
		t.Error("Enabled should default to true")
	}
}

// A roster read that fails must not take the board down with it: the names are
// decoration, the standings are the point.
func TestLeaderboardSurvivesARosterFailure(t *testing.T) {
	fs := &rosterFailStore{leaderboardStore: leaderboardStore{
		board: []store.AuthorScore{{Author: "alice", Total: 10}},
	}}
	s := NewServer(Deps{Store: fs, Config: func() config.Config { return config.Config{} }})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/leaderboard", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 despite the roster failing", rec.Code)
	}
	var got leaderboardResp
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Entries) != 1 || got.Entries[0].Author != "alice" {
		t.Errorf("entries = %+v, want the board regardless", got.Entries)
	}
}

type rosterFailStore struct{ leaderboardStore }

func (f *rosterFailStore) ListAuthors(context.Context, string, string) ([]store.Author, error) {
	return nil, context.DeadlineExceeded
}

// A hidden board serves no standings. Returning them alongside enabled=false
// said two things at once, and left a client that ignored the flag rendering a
// page the operator had switched off.
func TestDisabledLeaderboardServesNoEntries(t *testing.T) {
	fs := &leaderboardStore{board: []store.AuthorScore{{Author: "alice", Total: 135}}}
	cfg := config.Config{Scoring: config.ScoringSettings{Mode: config.ScoringDisabled}}

	got := getLeaderboard(t, fs, cfg, "/api/leaderboard")
	if got.Enabled {
		t.Error("Enabled should be false when scoring is disabled")
	}
	if len(got.Entries) != 0 {
		t.Errorf("entries = %+v, want none for a hidden board", got.Entries)
	}
}

// leaderboard-only keeps the standings: stopping the measuring is not a reason
// to hide the points already earned.
func TestLeaderboardOnlyStillServesStandings(t *testing.T) {
	fs := &leaderboardStore{board: []store.AuthorScore{{Author: "alice", Total: 135}}}
	cfg := config.Config{Scoring: config.ScoringSettings{Mode: config.ScoringLeaderboardOnly}}

	got := getLeaderboard(t, fs, cfg, "/api/leaderboard")
	if !got.Enabled {
		t.Error("leaderboard-only must keep the board visible")
	}
	if got.Mode != config.ScoringLeaderboardOnly {
		t.Errorf("mode = %q, want it reported so the page can say the board is paused", got.Mode)
	}
	if len(got.Entries) != 1 {
		t.Errorf("entries = %+v, want the existing standings", got.Entries)
	}
}
