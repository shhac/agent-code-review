package dashboard

// This file is the queue read surface: the pure status-derivation layer
// (claimStatus and friends — reviewlog.go's header shares claimStatus so the
// two surfaces cannot disagree on the lease boundary) and the GET handler.
// The write surface lives in queue.go.

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shhac/agent-code-review/internal/store"
)

// queueView is a Candidate plus the display status the frontend keys its
// badges on. The store has no status column anymore; "reviewing" is derived
// from a live claim, "held" from the eligibility hold; everything else in
// the queue is "queued".
type queueView struct {
	store.Candidate
	Status string `json:"status"` // queued|reviewing|held
	// Steering is the author's instruction for the next review of this PR,
	// if one is set. Carried on the row rather than fetched per candidate so
	// the UI can render the queue in one request.
	Steering *store.Steering `json:"steering,omitempty"`
}

// claimStatus maps the shared predicates (store.Candidate.ClaimActive and
// .Held) to the dashboard's status vocabulary: a live claim is "reviewing",
// an eligibility hold is "held", anything else (including a stale claim the
// the dispatcher will reclaim once the lease ages out) is "queued". The queue badges and the review-log
// header both derive from this one helper so they cannot disagree on the
// lease boundary.
func claimStatus(c store.Candidate, now time.Time, staleAfter time.Duration) string {
	if c.ClaimActive(now, staleAfter) {
		return "reviewing"
	}
	if c.Held(now) {
		return "held"
	}
	return "queued"
}

// viewQueue derives each candidate's display status and attaches any steering.
// Pure: unit-tested.
func viewQueue(candidates []store.Candidate, now time.Time, staleAfter time.Duration, steering []store.Steering) []queueView {
	byPR := make(map[string]*store.Steering, len(steering))
	for i := range steering {
		byPR[steeringKey(steering[i].Repo, steering[i].Number)] = &steering[i]
	}
	out := make([]queueView, 0, len(candidates))
	for _, c := range candidates {
		v := queueView{Candidate: c, Status: claimStatus(c, now, staleAfter)}
		v.Steering = byPR[steeringKey(c.Repo, c.Number)]
		out = append(out, v)
	}
	return out
}

// queueCounts is the fixed header-badge shape: waiting vs in-flight vs on
// hold, always summing to Total. A typed struct so a future status can't
// silently create a key nobody reads.
type queueCounts struct {
	Total     int `json:"total"`
	Queued    int `json:"queued"`
	Reviewing int `json:"reviewing"`
	Held      int `json:"held"`
}

type queueResp struct {
	Candidates []queueView `json:"candidates"`
	Counts     queueCounts `json:"counts"`
}

type queueAddResp struct {
	Queued bool   `json:"queued"`
	Title  string `json:"title"`
	Author string `json:"author"`
}

type queueRemoveResp struct {
	Removed bool `json:"removed"`
}

type queuePromoteResp struct {
	Promoted bool `json:"promoted"`
}

type queueReorderResp struct {
	Reordered bool `json:"reordered"`
}

// countQueue tallies views by display status. Pure: unit-tested with
// viewQueue so the badge counts and per-row statuses cannot disagree.
func countQueue(views []queueView) queueCounts {
	counts := queueCounts{Total: len(views)}
	for _, v := range views {
		switch v.Status {
		case "queued":
			counts.Queued++
		case "reviewing":
			counts.Reviewing++
		case "held":
			counts.Held++
		}
	}
	return counts
}

func (s *Server) listQueue(w http.ResponseWriter, r *http.Request) {
	serveGet(s, w, r, func(ctx context.Context) (queueResp, error) {
		candidates, err := s.store.ListQueue(ctx, "")
		if err != nil {
			return queueResp{}, err
		}
		steering, err := s.store.ListSteering(ctx)
		if err != nil {
			return queueResp{}, err
		}
		views := viewQueue(candidates, time.Now(), s.config().LeaseWindow(), steering)
		return queueResp{Candidates: views, Counts: countQueue(views)}, nil
	})
}

// steeringKey identifies a PR the way the steering table does: repo casing is
// preserved by the store, so matching folds it here rather than assuming the
// two rows agree on it.
func steeringKey(repo string, number int) string {
	return strings.ToLower(repo) + "#" + strconv.Itoa(number)
}
