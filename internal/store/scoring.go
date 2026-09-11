package store

import "time"

// Score provenance.
const (
	ScoreDerived = "derived" // computed from the configured rules
	ScoreManual  = "manual"  // an operator set it by hand
)

// DiffStats are a PR's line counts: what GitHub reports, and what survived
// exclusion of generated and vendored files.
//
// Both are kept because they answer different questions. Raw is what the PR
// page says and what a human will compare against; Scored is what the points
// were computed from. A row that reports only one of them either disagrees
// with GitHub for no visible reason or cannot explain its own score.
type DiffStats struct {
	Additions       int    `json:"additions"`
	Deletions       int    `json:"deletions"`
	ChangedFiles    int    `json:"changed_files"`
	ScoredAdditions int    `json:"scored_additions"`
	ScoredDeletions int    `json:"scored_deletions"`
	ExcludedFiles   int    `json:"excluded_files"`
	DiffSHA         string `json:"diff_sha"` // the revision these figures describe
}

// Recorded reports whether a diff was ever fetched for this review.
//
// DiffSHA is stamped from the fetch's own headRefOid on every path that
// records figures, including the truncated one, so an empty one means no fetch
// happened rather than a PR with nothing in it. The distinction matters
// because the alternative reading scores a real PR as zero: a rate-limited
// fetch leaves 0 additions and 0 deletions on the row, which is churn 0, which
// is a legitimate score of nothing for a PR whose every line was generated.
// Absent evidence and evidence of absence are not the same fact, and only one
// of them is worth zero points.
func (d DiffStats) Recorded() bool { return d.DiffSHA != "" }

// Stale reports whether these figures describe a revision other than headSHA.
//
// A PR's head can advance while a review runs (Complete's DELETE is gated on
// head_sha for exactly that reason), and the file stats are fetched at claim
// time. Crediting lines from a newer revision to a review that never saw them
// would be inventing a number. Named rather than inlined because BOTH the
// scoring decision and the log line that explains it have to agree about what
// stale means.
//
// An empty DiffSHA is not stale: it means no figures were recorded at all.
func (d DiffStats) Stale(headSHA string) bool {
	return d.DiffSHA != "" && d.DiffSHA != headSHA
}

// ScoreRecord is a frozen score plus the provenance needed to audit it.
//
// Score is a pointer because NULL (never scored) and 0 (scored, worth nothing)
// are different facts: a PR whose every line is generated legitimately earns
// zero, and a fetch that failed at completion legitimately earns nothing yet.
// Conflating them would let a recompute sweep skip the rows that most need it.
type ScoreRecord struct {
	Score  *int   `json:"score,omitempty"`
	Source string `json:"source,omitempty"` // ScoreDerived | ScoreManual
	Rules  string `json:"rules,omitempty"`  // ruleset hash the score was computed under
	// Bucket is the size tier the score came from, frozen with it. Stored
	// rather than re-derived on read for the same reason the score is: the row
	// may have been scored under a ruleset whose tiers have since moved, and a
	// bucket recomputed under today's rules would explain a number that today's
	// rules did not produce.
	Bucket  string    `json:"bucket,omitempty"`
	Note    string    `json:"note,omitempty"` // why, for a manual correction
	Attempt *int      `json:"attempt,omitempty"`
	At      time.Time `json:"at,omitempty"`
}

// Scored reports whether a score has actually been recorded.
func (s ScoreRecord) Scored() bool { return s.Score != nil }

// Points is the score, or 0 when unscored. For callers that are summing and
// have already decided an unscored row contributes nothing.
func (s ScoreRecord) Points() int {
	if s.Score == nil {
		return 0
	}
	return *s.Score
}

// ReviewRef addresses ONE history row.
//
// history has no primary key and no SQL-visible row identity: ReviewLogKey is
// a Go-side SHA-256 computed in the scanner, so it cannot appear in a WHERE
// clause (duckdb_history.go says the same thing about EstimateCosts, which is
// set-based for this reason). ReviewedAt is stamped time.Now() per row, so
// (repo, number, reviewed_at) is unique in practice.
//
// It is a NATURAL key, not an enforced one, which is why every write through
// it checks how many rows it touched instead of trusting that it was one.
// History rows are written at microsecond precision (tsExact) rather than the
// second-truncated form used for holds and claims, so two reviews of one PR
// would have to land in the same microsecond to collide; a row from an older
// build carries second precision and still compares correctly, because SQL
// compares TIMESTAMP values rather than their spellings.
type ReviewRef struct {
	Repo       string
	Number     int
	ReviewedAt time.Time
}

// Ref addresses this review's own history row.
func (r Review) Ref() ReviewRef {
	return ReviewRef{Repo: r.Repo, Number: r.Number, ReviewedAt: r.ReviewedAt}
}

// ScoreQuery selects history rows for a scoring sweep.
//
// Missing and StaleRules are the two reasons a row needs revisiting, and they
// are deliberately separate: Missing is "this never got a score" (a fetch
// failed), StaleRules is "this was scored under rules we have since changed".
// Only the second is a decision about somebody's points, so only the second
// should ever be run without thinking.
type ScoreQuery struct {
	Repo string
	// Number narrows to one PR. Without it the only caller that wants a single
	// PR had to pull its repo's entire scored history and discard the rest in
	// Go, while the store already had prWhere for exactly this predicate.
	Number  int
	Author  string
	Since   time.Time
	Missing bool // only rows with no score
	// StaleRules selects rows whose recorded ruleset hash is no longer the one
	// their repo would be scored under now. Keyed BY REPO, with "" holding the
	// hash for any repo that has no scoring override of its own.
	//
	// A single hash cannot express this. Rules are per-repo by construction
	// (scoring.repos), so comparing every row against one hash marked every
	// row in an overridden repo permanently stale: its rows were scored under
	// the override's hash and were being measured against the global one, so a
	// sweep would select them, rewrite them to the identical value, and select
	// them again forever.
	StaleRules    map[string]string
	IncludeManual bool // by default a manual score is left alone
	Limit         int
}

// LeaderboardQuery narrows the aggregate.
type LeaderboardQuery struct {
	Repo  string
	Since time.Time
	Limit int
}

// AuthorScore is one row of the leaderboard.
//
// Reviews counts rows that CONTRIBUTED to Total (scored ones), not every
// review the author has ever had: an unscored row is absent from the sum, so
// counting it here would make the mean lie.
type AuthorScore struct {
	Author    string `json:"author"`
	Total     int    `json:"total"`
	Reviews   int    `json:"reviews"`
	Approvals int    `json:"approvals"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}
