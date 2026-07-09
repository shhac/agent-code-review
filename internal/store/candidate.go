package store

import "time"

// Candidate is a PR in the review queue. A candidate exists exactly while
// review work is pending — completion moves it into history.
type Candidate struct {
	Repo         string     `json:"repo"`
	Number       int        `json:"number"`
	Type         string     `json:"type"` // "new" | "refreshed"
	Title        string     `json:"title"`
	Author       string     `json:"author"`
	URL          string     `json:"url"`
	HeadSHA      string     `json:"head_sha"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	QueuePos     int        `json:"queue_pos"`
	DiscoveredAt time.Time  `json:"discovered_at"`        // first time discovery saw this pending work; never bumped by later sweeps
	ClaimedAt    *time.Time `json:"claimed_at,omitempty"` // set while an engine reviews it; stale claims are reclaimable
	ClaimHost    string     `json:"claim_host,omitempty"` // which daemon holds the claim — boot reconciliation clears claims whose pid died on this host
	ClaimPID     int        `json:"claim_pid,omitempty"`
	Source       string     `json:"source"`                // SourceDiscovered | SourceManual
	WorkDir      string     `json:"work_dir,omitempty"`    // engine scratch workspace, set at claim time; <work_dir>/agent.log is the live review log
	EligibleAt   *time.Time `json:"eligible_at,omitempty"` // eligibility hold: the scheduler skips this row until then; nil = eligible now
	HoldReason   string     `json:"hold_reason,omitempty"` // HoldCooldown | HoldSettling while a hold is set
}

// QueuePosition is one member of a complete queue ordering.
type QueuePosition struct {
	Repo     string
	Number   int
	Position int
}

// Lease identifies one claim attempt: when, by whom (host+pid, for crash
// reconciliation), the engine workspace, and how old an existing claim must
// be before it counts as abandoned and may be taken over.
type Lease struct {
	At         time.Time
	WorkDir    string
	Host       string
	PID        int
	StaleAfter time.Duration
}

// Hold reasons: why a queued candidate is not yet eligible for review.
const (
	HoldCooldown = "cooldown" // we reviewed this PR recently (candidates.rereview_cooldown)
	HoldSettling = "settling" // the PR was updated too recently (candidates.quiet_period)
)

// Candidate sources. Manual adds bypass the pre-review candidacy check so
// explicit re-review requests and draft reviews always go through.
const (
	SourceDiscovered = "discovered"
	SourceManual     = "manual"
)

// Candidate types.
const (
	TypeNew       = "new"
	TypeRefreshed = "refreshed"
)

// ClaimActive reports whether c's claim is a live lease: an engine claimed it
// within the window. False for unclaimed rows and for stale claims (a crashed
// daemon's leftovers — eligible for reclaim). This is THE lease predicate:
// the scheduler's reclaim filter and the dashboard's "reviewing" badge are
// both defined in terms of it, so they cannot disagree.
func (c Candidate) ClaimActive(now time.Time, window time.Duration) bool {
	return c.ClaimedAt != nil && now.Sub(*c.ClaimedAt) <= window
}

// Held reports whether c is under an eligibility hold: queued, visible, but
// not yet reviewable. THE hold predicate — the scheduler's eligibility filter
// and the dashboard's "on hold" badge are both defined in terms of it, so
// they cannot disagree.
func (c Candidate) Held(now time.Time) bool {
	return c.EligibleAt != nil && now.Before(*c.EligibleAt)
}
