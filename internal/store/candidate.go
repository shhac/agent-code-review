package store

import "time"

// Candidate is a PR in the review queue. A candidate exists exactly while
// review work is pending; completion moves it into history.
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
	ClaimHost    string     `json:"claim_host,omitempty"` // which daemon holds the claim; boot reconciliation clears claims whose pid died on this host
	ClaimPID     int        `json:"claim_pid,omitempty"`
	Source       string     `json:"source"`             // SourceDiscovered | SourceManual
	WorkDir      string     `json:"work_dir,omitempty"` // engine scratch workspace, set at claim time; <work_dir>/agent.log is the live review log
	// Holds maps each named eligibility hold on this row to the instant it
	// expires. The row is reviewable once every one of them is in the past,
	// so holds compose upward: a new kind of hold can defer a PR further, but
	// can never make it eligible sooner than another hold already made it.
	// That monotonicity is why an author-triggered hold is safe to sit
	// alongside the policy ones.
	//
	// Expired entries are left in place rather than swept: they are already
	// not holds, and the alternative is every writer having to know which
	// names it is allowed to retire.
	Holds map[string]time.Time `json:"holds,omitempty"`
	// EditingSince is when the current steering-editor session began, or nil
	// when nobody has it open. Deliberately NOT a hold: it defers nothing by
	// itself, it is only the anchor the renewal cap measures from, so that an
	// editor left open cannot keep re-imposing HoldEditing forever.
	EditingSince *time.Time `json:"editing_since,omitempty"`
	// Steering is the instruction shaping this PR's next review, if one is
	// set. A field on the row rather than a joined entity: it shares the row's
	// key and lifetime exactly, so it goes when the row goes.
	Steering *Steering `json:"steering,omitempty"`
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

// Hold names: why a queued candidate is not yet eligible for review. Each is
// one key of Candidate.Holds, and each has exactly one writer, which is what
// lets a discovery sweep rewrite its own two without touching anybody else's.
const (
	HoldCooldown = "cooldown" // we reviewed this PR recently (candidates.rereview_cooldown)
	HoldSettling = "settling" // the PR was updated too recently (candidates.quiet_period)
	HoldEditing  = "editing"  // an author has the steering editor open (candidates.steering_hold)
)

// Candidate sources. Manual adds bypass the pre-review candidacy check so
// explicit re-review requests and draft reviews always go through.
const (
	SourceDiscovered = "discovered"
	SourceManual     = "manual"
)

// Candidate types.
const (
	TypeNew        = "new"
	TypeRefreshed  = "refreshed"
	TypeDiscussion = "discussion"
)

// ClaimActive reports whether c's claim is a live lease: an engine claimed it
// within the window. False for unclaimed rows and for stale claims (a crashed
// daemon's leftovers, eligible for reclaim). This is THE lease predicate:
// the scheduler's reclaim filter and the dashboard's "reviewing" badge are
// both defined in terms of it, so they cannot disagree.
func (c Candidate) ClaimActive(now time.Time, window time.Duration) bool {
	return c.ClaimedAt != nil && now.Sub(*c.ClaimedAt) <= window
}

// Held reports whether c is under an eligibility hold: queued, visible, but
// not yet reviewable. THE hold predicate: the scheduler's eligibility filter
// and the dashboard's "on hold" badge are both defined in terms of it, so
// they cannot disagree.
func (c Candidate) Held(now time.Time) bool {
	until, _ := c.EffectiveReady()
	return now.Before(until)
}

// EffectiveReady is the instant every hold has expired, and the name of the
// hold that decides it. The zero time and "" when nothing holds the row.
//
// MAX rather than any other combination: a hold defers, and one hold must not
// be able to undo another's deferral. Ties break on the name so the reported
// reason is stable across calls, since Go randomises map iteration and this
// value is rendered in the dashboard.
func (c Candidate) EffectiveReady() (time.Time, string) {
	var until time.Time
	var name string
	for n, t := range c.Holds {
		if t.After(until) || (t.Equal(until) && !t.IsZero() && n < name) {
			until, name = t, n
		}
	}
	return until, name
}
