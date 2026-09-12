package dashboard

// The leaderboard surface: /api/leaderboard ranks authors by the points their
// reviewed PRs have earned.
//
// Unlike /api/stats and /api/metrics, which pull a 24h window of rows and
// reduce them in Go, this aggregates in SQL. The window is the difference:
// those summarise a day and ask several varied questions of it, where this one
// asks a single question of ALL history. Pulling every row across to count it
// would be the wrong shape against a columnar store, and the aggregate is
// deliberately not denormalised onto the author roster (see store.Leaderboard
// for why).

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/shhac/agent-code-review/internal/store"
)

// leaderboardEntry is one author's standing.
//
// Reviews counts only the rows that CONTRIBUTED to Total. A review whose score
// could not be computed is absent from the sum, so counting it here would make
// the average per review lie about the rows behind it.
type leaderboardEntry struct {
	Rank int `json:"rank"`
	// Name is the roster's display name, absent for an author who has PRs
	// reviewed but no roster row (which authors.unlisted exists to allow).
	Name string `json:"name,omitempty"`
	// Embedded rather than transcribed: the aggregate's columns reached the
	// API through a field-by-field copy that had to be edited in lockstep with
	// the SQL. The CLI's leaderboard already embeds it this way.
	store.AuthorScore
}

type leaderboardResp struct {
	// Enabled is false only when scoring is fully DISABLED, so the page can say
	// so rather than render an empty board that looks like nobody has earned
	// anything. Leaderboard-only mode leaves it true: the measuring has
	// stopped, but the points already earned are still worth showing.
	Enabled bool   `json:"enabled"`
	Mode    string `json:"mode"`
	Days    int    `json:"days"`
	Repo    string `json:"repo,omitempty"`
	// Sort is the measure this board is ranked by, echoed back because the
	// page draws its magnitude bar on that column and an unrecognised value
	// falls back to the total rather than erroring.
	Sort    string             `json:"sort"`
	Entries []leaderboardEntry `json:"entries"`
}

// handleLeaderboard ranks authors by one of several measures.
//
// days=0 means all of history, which is the default: a leaderboard that
// silently covered only the last day would flatter whoever shipped yesterday.
//
// The sort is applied in SQL, not in the page, because rank and the row limit
// both depend on it. Re-sorting a top-100-by-total in the browser would show
// the hundred biggest contributors arranged by median, which is a different
// and much less interesting set of people than the hundred best medians.
func (s *Server) handleLeaderboard(w http.ResponseWriter, r *http.Request) {
	serveGet(s, w, r, func(ctx context.Context) (leaderboardResp, error) {
		q := r.URL.Query()
		repo := q.Get("repo")
		days, _ := strconv.Atoi(q.Get("days"))
		sort := q.Get("sort")
		if !store.ValidLeaderSort(sort) {
			sort = store.LeaderTotal
		}

		resp := leaderboardResp{
			Enabled: s.config().LeaderboardVisible(repo),
			Mode:    s.config().ScoringMode(repo),
			Days:    days,
			Repo:    repo,
			Sort:    sort,
		}
		// Nothing to serve for a board that is switched off. Returning
		// standings alongside enabled=false said two things at once and left a
		// client that ignored the flag rendering a page the operator hid.
		if !resp.Enabled {
			resp.Entries = []leaderboardEntry{}
			return resp, nil
		}

		lq := store.LeaderboardQuery{Repo: repo, Limit: 100, Sort: sort}
		if days > 0 {
			lq.Since = time.Now().AddDate(0, 0, -days)
		}
		board, err := s.store.Leaderboard(ctx, lq)
		if err != nil {
			return leaderboardResp{}, err
		}
		resp.Entries = s.rankEntries(ctx, board)
		return resp, nil
	})
}

// rankEntries numbers the board and attaches the display name the roster
// knows, so the page can show a person rather than only a handle.
//
// A missing roster row is not an error: somebody can have PRs reviewed without
// ever being listed (that is what authors.unlisted is for), and their standing
// must still appear.
func (s *Server) rankEntries(ctx context.Context, board []store.AuthorScore) []leaderboardEntry {
	names := map[string]string{}
	if authors, err := s.store.ListAuthors(ctx, "", ""); err == nil {
		for _, a := range authors {
			if a.Name != "" {
				names[a.GitHubHandle] = a.Name
			}
		}
	}
	out := make([]leaderboardEntry, 0, len(board))
	for i, a := range board {
		out = append(out, leaderboardEntry{Rank: i + 1, Name: names[a.Author], AuthorScore: a})
	}
	return out
}
