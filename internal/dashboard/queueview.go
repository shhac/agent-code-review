package dashboard

// This file is the queue read surface: the pure status-derivation layer
// (claimStatus and friends — reviewlog.go's header shares claimStatus so the
// two surfaces cannot disagree on the lease boundary) and the GET handler.
// The write surface lives in queue.go.

import (
	"net/http"
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
}

// claimStatus maps the shared predicates (store.Candidate.ClaimActive and
// .Held) to the dashboard's status vocabulary: a live claim is "reviewing",
// an eligibility hold is "held", anything else (including a stale claim the
// next cycle will reclaim) is "queued". The queue badges and the review-log
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

// viewQueue derives each candidate's display status. Pure: unit-tested.
func viewQueue(candidates []store.Candidate, now time.Time, staleAfter time.Duration) []queueView {
	out := make([]queueView, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, queueView{Candidate: c, Status: claimStatus(c, now, staleAfter)})
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
	ctx, cancel := reqCtx(r, 10*time.Second)
	defer cancel()
	candidates, err := s.store.ListQueue(ctx, "")
	if err != nil {
		s.fail(w, err)
		return
	}
	views := viewQueue(candidates, time.Now(), s.config().LeaseWindow())
	writeJSON(w, http.StatusOK, queueResp{Candidates: views, Counts: countQueue(views)})
}
