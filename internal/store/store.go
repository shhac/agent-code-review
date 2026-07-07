// Package store persists the review queue, review history, and run-lock. The
// only backend today is DuckDB via the `duckdb` CLI (subprocess, CGO-free —
// see duckdb.go), chosen so the binary still cross-compiles through the family
// release pipeline. Everything is behind the Store interface so a second
// backend (or a future embedded driver) can drop in.
package store

import (
	"context"
	"fmt"
	"time"
)

// Candidate is a PR in the review queue.
type Candidate struct {
	Repo         string    `json:"repo"`
	Number       int       `json:"number"`
	Type         string    `json:"type"` // "new" | "refreshed"
	Title        string    `json:"title"`
	Author       string    `json:"author"`
	URL          string    `json:"url"`
	HeadSHA      string    `json:"head_sha"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	QueuePos     int       `json:"queue_pos"`
	Status       string    `json:"status"` // queued|reviewing|reviewed|skipped|error
	DiscoveredAt time.Time `json:"discovered_at"`
}

// Review records one completed engine review of a PR at a specific head SHA.
type Review struct {
	Repo       string    `json:"repo"`
	Number     int       `json:"number"`
	HeadSHA    string    `json:"head_sha"`
	Verdict    string    `json:"verdict"` // APPROVE|COMMENT|ERROR
	Engine     string    `json:"engine"`
	ReviewedAt time.Time `json:"reviewed_at"`
}

// Approver is one entry in a repo's approval allow-list. Approval is decided
// per repo: a PR author may be approved only if listed for that PR's repo (or
// for the wildcard repo "*"). The list lives in the store, not config, so it
// can be managed at runtime and vary per repo.
type Approver struct {
	Repo         string `json:"repo"`
	GitHubHandle string `json:"github_handle"`
	Name         string `json:"name,omitempty"`
	Email        string `json:"email,omitempty"`
	SlackID      string `json:"slack_id,omitempty"`
}

// WildcardRepo, when used as an Approver.Repo, applies to every repo.
const WildcardRepo = "*"

// Run is one review cycle, used as the advisory run-lock.
type Run struct {
	ID         string     `json:"id"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Status     string     `json:"status"` // running|done|failed
	Host       string     `json:"host"`
	PID        int        `json:"pid"`
}

// Filter narrows ListCandidates. Zero-value fields are ignored.
type Filter struct {
	Status string
	Repo   string
}

// Statuses.
const (
	StatusQueued    = "queued"
	StatusReviewing = "reviewing"
	StatusReviewed  = "reviewed"
	StatusSkipped   = "skipped"
	StatusError     = "error"
)

// Candidate types.
const (
	TypeNew       = "new"
	TypeRefreshed = "refreshed"
)

// Store is the persistence contract.
type Store interface {
	// Init applies the schema (idempotent).
	Init(ctx context.Context) error

	UpsertCandidate(ctx context.Context, c Candidate) error
	ListCandidates(ctx context.Context, f Filter) ([]Candidate, error)
	GetCandidate(ctx context.Context, repo string, number int) (Candidate, bool, error)
	SetStatus(ctx context.Context, repo string, number int, status string) error
	SetQueuePos(ctx context.Context, repo string, number int, pos int) error
	RemoveCandidate(ctx context.Context, repo string, number int) error

	// LastReview returns the most recent review for a PR, if any.
	LastReview(ctx context.Context, repo string, number int) (Review, bool, error)
	RecordReview(ctx context.Context, r Review) error
	// ListReviews returns review history, most recent first, capped at limit.
	ListReviews(ctx context.Context, limit int) ([]Review, error)

	// Approver allow-list (per repo, "*" = all repos).
	AddApprover(ctx context.Context, a Approver) error
	RemoveApprover(ctx context.Context, repo, handle string) error
	ListApprovers(ctx context.Context, repo string) ([]Approver, error)
	// IsApprover reports whether handle may be approved for repo, matching the
	// repo exactly or the wildcard "*".
	IsApprover(ctx context.Context, repo, handle string) (bool, error)

	// ActiveRun returns an unfinished run more recent than staleAfter, if any.
	ActiveRun(ctx context.Context, staleAfter time.Duration) (Run, bool, error)
	StartRun(ctx context.Context, r Run) error
	FinishRun(ctx context.Context, id string, status string) error
	// ListRuns returns cycle history, most recent first, capped at limit.
	ListRuns(ctx context.Context, limit int) ([]Run, error)

	Close() error
}

// Open returns a Store for the given engine + path. Only "duckdb" (and its
// empty-string default) is wired today.
func Open(engine, path string) (Store, error) {
	switch engine {
	case "", "duckdb":
		return newDuckDB(path)
	default:
		return nil, fmt.Errorf("Unknown store engine: %q. Valid: duckdb", engine)
	}
}
