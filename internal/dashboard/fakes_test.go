package dashboard

import (
	"context"
	"strings"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/prref"
	"github.com/shhac/agent-code-review/internal/store"
)

// fakeStore is the dashboard's one store fake.
//
// It embeds dashboardStore rather than store.Store: an unimplemented call
// still panics loudly, but the panic surface is what the SERVER uses rather
// than the application's whole persistence API. That distinction is the point
// — it was four fakes, three of them embedding store.Store, and the widest
// one had to be extended for every new store method whether or not the tests
// in that file called it.
//
// Seeded reads are fields; writes are recorded in slices. Tests set only what
// they care about; the zero value is an empty store that records everything.
type fakeStore struct {
	dashboardStore // anything not implemented here panics

	// Seeded reads.
	queue     []store.Candidate
	reviews   []store.Review
	byKey     map[string]store.Review // review-log lookups, keyed by log key
	logReview store.Review            // single-review fallback for ReviewByLogKey
	byLogin   map[string]store.Author // tailnet login -> roster row
	groups    map[string]string       // handle -> group
	tokens    map[bool]int64          // keyed by since.IsZero()

	// Recorded writes.
	enqueued  []store.Candidate
	dequeued  []prref.Ref
	promoted  []prref.Ref
	positions []store.QueuePosition
	steered   []steerCall
	cleared   []prref.Ref

	// Injected failures.
	reorderErr error
	sinceErr   error

	since time.Time
}

// steerCall records one SetSteering with the PR it named. The stored value
// carries no repo/number of its own, so the test has to keep them.
type steerCall struct {
	store.Steering
	repo   string
	number int
}

func (f *fakeStore) ListQueue(context.Context, string) ([]store.Candidate, error) {
	return f.queue, nil
}

// QueuedPR matches repo EXACTLY, as the store's prWhere does. A fake that
// ignored its arguments here is what hid a Go-side case fold that the SQL
// filter made unreachable.
func (f *fakeStore) QueuedPR(_ context.Context, repo string, number int) (store.Candidate, bool, error) {
	for _, c := range f.queue {
		if c.Repo == repo && c.Number == number {
			return c, true, nil
		}
	}
	return store.Candidate{}, false, nil
}

func (f *fakeStore) Enqueue(_ context.Context, c store.Candidate) error {
	f.enqueued = append(f.enqueued, c)
	return nil
}

func (f *fakeStore) Dequeue(_ context.Context, repo string, number int) error {
	f.dequeued = append(f.dequeued, prref.Ref{Repo: repo, Number: number})
	return nil
}

func (f *fakeStore) Promote(_ context.Context, repo string, number int) error {
	f.promoted = append(f.promoted, prref.Ref{Repo: repo, Number: number})
	return nil
}

func (f *fakeStore) Reorder(_ context.Context, positions []store.QueuePosition) error {
	if f.reorderErr != nil {
		return f.reorderErr
	}
	f.positions = positions
	return nil
}

func (f *fakeStore) LastOutcome(context.Context, string, int) (store.Review, bool, error) {
	return store.Review{}, false, nil
}

func (f *fakeStore) ListReviews(context.Context, int) ([]store.Review, error) {
	return f.reviews, nil
}

func (f *fakeStore) ListReviewsSince(_ context.Context, since time.Time) ([]store.Review, error) {
	if f.sinceErr != nil {
		return nil, f.sinceErr
	}
	f.since = since
	return f.reviews, nil
}

func (f *fakeStore) FreshTokens(_ context.Context, since time.Time) (int64, error) {
	return f.tokens[since.IsZero()], nil
}

func (f *fakeStore) ReviewByLogKey(_ context.Context, _ string, _ int, key string) (store.Review, bool, error) {
	if f.byKey != nil {
		r, ok := f.byKey[key]
		return r, ok, nil
	}
	if f.logReview.LogKey == "" {
		return store.Review{}, false, nil
	}
	return f.logReview, true, nil
}

func (f *fakeStore) AuthorGroup(_ context.Context, _, handle string) (config.Membership, error) {
	g, ok := f.groups[handle]
	if !ok {
		return config.Membership{}, nil
	}
	return config.Membership{Group: g, Repo: config.WildcardRepo}, nil
}

func (f *fakeStore) AuthorByTailscaleLogin(_ context.Context, login string) (store.Author, bool, error) {
	for k, a := range f.byLogin {
		if strings.EqualFold(k, login) {
			return a, true, nil
		}
	}
	return store.Author{}, false, nil
}

func (f *fakeStore) SetSteering(_ context.Context, repo string, number int, st store.Steering) error {
	f.steered = append(f.steered, steerCall{Steering: st, repo: repo, number: number})
	return nil
}

func (f *fakeStore) ClearSteering(_ context.Context, repo string, number int) error {
	f.cleared = append(f.cleared, prref.Ref{Repo: repo, Number: number})
	return nil
}
