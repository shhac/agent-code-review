package store

import "time"

// Steering is one author-supplied instruction shaping the next review of a PR.
type Steering struct {
	Repo    string    `json:"repo"`
	Number  int       `json:"number"`
	Message string    `json:"message"`
	SetBy   string    `json:"set_by"`
	SetAt   time.Time `json:"set_at"`
}

// SteeringMaxLen bounds one message. Steering is a nudge that rides in the
// review prompt ("the migration is behind a flag, focus on rollback"), not a
// spec: an unbounded field would let a prompt be rewritten wholesale by
// somebody who is only authorised to influence their own review.
const SteeringMaxLen = 2000
