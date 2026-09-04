package dashboard

// This file is the queue read surface: the pure status-derivation layer
// (claimStatus and friends — reviewlog.go's header shares claimStatus so the
// two surfaces cannot disagree on the lease boundary) and the GET handler.
// The write surface lives in queue.go.

import (
	"context"
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
	// MaySteer is whether the CURRENT viewer may steer this PR, decided by the
	// same viewer.maySteer the write path enforces. Sent per row so the UI can
	// offer the control exactly where it would be accepted, without
	// reimplementing the rule in TypeScript where it could drift.
	MaySteer bool `json:"may_steer"`
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

// viewQueue derives each candidate's display status. Steering needs no
// attaching: it is a field of the candidate, so ListQueue already carried it.
// Pure: unit-tested.
func viewQueue(candidates []store.Candidate, now time.Time, staleAfter time.Duration, v viewer) []queueView {
	out := make([]queueView, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, queueView{
			Candidate: c,
			Status:    claimStatus(c, now, staleAfter),
			MaySteer:  v.maySteer(c.Author),
		})
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
	Title  string `json:"title,omitempty"`
	Author string `json:"author,omitempty"`
	// Steered says whether an accompanying steering message was applied.
	// SteeringRefused says why not, when one was sent and rejected: the add
	// still happened, so the caller has to be told which half it got.
	Steered         bool   `json:"steered,omitempty"`
	SteeringRefused string `json:"steering_refused,omitempty"`
}

// queuePreflightResp answers "what is this PR, and may I steer it" before the
// add. Advisory: the add re-resolves and re-checks.
type queuePreflightResp struct {
	Repo     string `json:"repo"`
	Number   int    `json:"number"`
	Title    string `json:"title"`
	Author   string `json:"author"`
	MaySteer bool   `json:"may_steer"`
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
		v, err := s.identify(ctx, r)
		if err != nil {
			return queueResp{}, err
		}
		views := viewQueue(candidates, time.Now(), s.config().LeaseWindow(), v)
		return queueResp{Candidates: views, Counts: countQueue(views)}, nil
	})
}
