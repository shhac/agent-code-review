package store

import "time"

// Steering is one author-supplied instruction shaping the next review of a PR.
// It carries no repo/number: it is a field of the Candidate that holds it, and
// giving it its own identity is what previously let a steering row and its
// queue row disagree about which PR they meant.
type Steering struct {
	Message string    `json:"message"`
	SetBy   string    `json:"set_by"`
	SetAt   time.Time `json:"set_at"`
}

// SteeringMaxLen bounds one message. Steering is a nudge that rides in the
// review prompt ("the migration is behind a flag, focus on rollback"), not a
// spec: an unbounded field would let a prompt be rewritten wholesale by
// somebody who is only authorised to influence their own review.
const SteeringMaxLen = 2000
