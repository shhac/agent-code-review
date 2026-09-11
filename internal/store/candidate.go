package store

import (
	"maps"
	"slices"
	"time"
)

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
	// Holds maps a name to an instant. The row is reviewable once every one of
	// them is in the past, so holds compose upward: a new name can defer a PR
	// further, but can never make it eligible sooner than another name already
	// made it. That monotonicity is why an author-triggered hold is safe to sit
	// alongside the policy ones.
	//
	// Expired entries are left in place rather than swept: they are already not
	// holds, and the alternative is every writer having to know which names it
	// is allowed to retire.
	//
	// Which is also why an instant that is ALWAYS past — when a thing began
	// rather than when it ends, MarkEditingSince below — belongs here rather
	// than in a column of its own. It defers nothing by arithmetic, not by
	// convention, and it gets the same per-name merge, the same atomic write
	// alongside the hold it describes, and the same retirement.
	Holds map[string]time.Time `json:"holds,omitempty"`
	// Steering is the instruction shaping this PR's next review, if one is
	// set. A field on the row rather than a joined entity: it shares the row's
	// key and lifetime exactly, so it goes when the row goes.
	Steering *Steering `json:"steering,omitempty"`
	// Additions/Deletions/ChangedFiles are what discovery saw, and they cost
	// nothing: `gh pr list --json` is one GraphQL query whatever fields it
	// names. These are the RAW totals; the figures a score is computed from
	// have generated files taken out and are fetched per-file at claim time.
	Additions    int `json:"additions,omitempty"`
	Deletions    int `json:"deletions,omitempty"`
	ChangedFiles int `json:"changed_files,omitempty"`
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
	HoldRetry    = "retry"    // an engine attempt failed; wait before trying again (candidates.error_backoff)
)

// MarkEditingSince is when the current steering-editor session began. It lives
// in Holds like everything else, but it is a MARK, not a hold: its instant is
// always in the past, so it can never win the MAX and can never defer the row.
// It exists so renewal can be capped, and it is written and retired in the same
// statement as HoldEditing, which is what stops the two disagreeing.
const MarkEditingSince = "editing-since"

// EditingNames are the entries one steering-editor session owns: the hold that
// defers the row and the mark that dates the session. Always written and
// cleared together.
var EditingNames = []string{HoldEditing, MarkEditingSince}

// DiscoveryHolds are the names a discovery sweep owns and may rewrite. Naming
// the set once is what lets a manual enqueue clear discovery's holds without
// reaching for holds it knows nothing about.
var DiscoveryHolds = []string{HoldCooldown, HoldSettling}

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
// be able to undo another's deferral.
//
// Sorted iteration with a strict After, so a tie goes to the first name in
// order. Go randomises map iteration and this name is rendered in the
// dashboard, so without a rule two equal holds would flicker between renders;
// making it a property of the walk beats a comparison that has to be reasoned
// about to be believed.
func (c Candidate) EffectiveReady() (time.Time, string) {
	var until time.Time
	var name string
	for _, n := range slices.Sorted(maps.Keys(c.Holds)) {
		if t := c.Holds[n]; t.After(until) {
			until, name = t, n
		}
	}
	return until, name
}
