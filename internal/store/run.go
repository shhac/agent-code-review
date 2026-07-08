package store

import "time"

// Run is one review cycle, used as the advisory run-lock.
type Run struct {
	ID         string     `json:"id"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Status     string     `json:"status"` // running|done|failed
	Host       string     `json:"host"`
	PID        int        `json:"pid"`
}
