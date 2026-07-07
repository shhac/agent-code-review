package discover

import (
	"context"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
)

// fakeStore stubs the Store interface; only LastReview carries test state.
type fakeStore struct {
	last    store.Review
	hasLast bool
}

func (f *fakeStore) Init(context.Context) error                             { return nil }
func (f *fakeStore) UpsertCandidate(context.Context, store.Candidate) error { return nil }
func (f *fakeStore) ListCandidates(context.Context, store.Filter) ([]store.Candidate, error) {
	return nil, nil
}
func (f *fakeStore) GetCandidate(context.Context, string, int) (store.Candidate, bool, error) {
	return store.Candidate{}, false, nil
}
func (f *fakeStore) SetStatus(context.Context, string, int, string) error { return nil }
func (f *fakeStore) SetQueuePos(context.Context, string, int, int) error  { return nil }
func (f *fakeStore) RemoveCandidate(context.Context, string, int) error   { return nil }
func (f *fakeStore) LastReview(context.Context, string, int) (store.Review, bool, error) {
	return f.last, f.hasLast, nil
}
func (f *fakeStore) RecordReview(context.Context, store.Review) error { return nil }
func (f *fakeStore) ListReviews(context.Context, int) ([]store.Review, error) {
	return nil, nil
}
func (f *fakeStore) ListReviewsSince(context.Context, time.Time) ([]store.Review, error) {
	return nil, nil
}
func (f *fakeStore) ListRuns(context.Context, int) ([]store.Run, error)     { return nil, nil }
func (f *fakeStore) AllowAuthor(context.Context, store.AllowedAuthor) error { return nil }
func (f *fakeStore) DenyAuthor(context.Context, string, string) error       { return nil }
func (f *fakeStore) ListAllowedAuthors(context.Context, string) ([]store.AllowedAuthor, error) {
	return nil, nil
}
func (f *fakeStore) IsAuthorAllowed(context.Context, string, string) (bool, error) {
	return false, nil
}
func (f *fakeStore) ActiveRun(context.Context, time.Duration) (store.Run, bool, error) {
	return store.Run{}, false, nil
}
func (f *fakeStore) StartRun(context.Context, store.Run) error       { return nil }
func (f *fakeStore) FinishRun(context.Context, string, string) error { return nil }
func (f *fakeStore) Close() error                                    { return nil }

func fixedNow() time.Time { return time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC) }

func newDiscoverer(fs *fakeStore) *Discoverer {
	d := New(config.Config{}, fs, nil)
	d.now = fixedNow
	return d
}

func openReq() []ghActor { return []ghActor{{Login: "reviewer"}} }

func TestClassifyNew(t *testing.T) {
	d := newDiscoverer(&fakeStore{})
	pr := ghPR{
		Number:         1,
		HeadRefOID:     "sha1",
		CreatedAt:      fixedNow().Add(-3 * 24 * time.Hour), // 3 days old
		ReviewRequests: openReq(),
	}
	c, ok, err := d.classify(context.Background(), "o/r", pr)
	if err != nil || !ok {
		t.Fatalf("expected a New candidate, ok=%v err=%v", ok, err)
	}
	if c.Type != store.TypeNew {
		t.Errorf("type = %q, want new", c.Type)
	}
}

func TestClassifyDraftRejected(t *testing.T) {
	d := newDiscoverer(&fakeStore{})
	pr := ghPR{Number: 1, IsDraft: true, ReviewRequests: openReq(), CreatedAt: fixedNow()}
	if _, ok, _ := d.classify(context.Background(), "o/r", pr); ok {
		t.Error("draft PR should not be a candidate")
	}
}

func TestClassifyNoReviewRequestRejected(t *testing.T) {
	d := newDiscoverer(&fakeStore{})
	pr := ghPR{Number: 1, CreatedAt: fixedNow()} // no review requested
	if _, ok, _ := d.classify(context.Background(), "o/r", pr); ok {
		t.Error("PR without an open review request should not be a candidate")
	}
}

func TestClassifyTooOldRejected(t *testing.T) {
	d := newDiscoverer(&fakeStore{})
	pr := ghPR{
		Number:         1,
		ReviewRequests: openReq(),
		CreatedAt:      fixedNow().Add(-20 * 24 * time.Hour), // 20d > 14d New window
	}
	if _, ok, _ := d.classify(context.Background(), "o/r", pr); ok {
		t.Error("PR older than the New window should not be a New candidate")
	}
}

func TestClassifyRefreshedOnDifferentSHA(t *testing.T) {
	fs := &fakeStore{hasLast: true, last: store.Review{HeadSHA: "old-sha"}}
	d := newDiscoverer(fs)
	pr := ghPR{
		Number:         2,
		HeadRefOID:     "new-sha",
		ReviewRequests: openReq(),
		Reviews:        []ghReview{{State: "APPROVED"}}, // already reviewed → not New
		CreatedAt:      fixedNow().Add(-10 * 24 * time.Hour),
	}
	c, ok, err := d.classify(context.Background(), "o/r", pr)
	if err != nil || !ok {
		t.Fatalf("expected a Refreshed candidate, ok=%v err=%v", ok, err)
	}
	if c.Type != store.TypeRefreshed {
		t.Errorf("type = %q, want refreshed", c.Type)
	}
}

func TestClassifyRefreshedSameSHARejected(t *testing.T) {
	fs := &fakeStore{hasLast: true, last: store.Review{HeadSHA: "same-sha"}}
	d := newDiscoverer(fs)
	pr := ghPR{
		Number:         2,
		HeadRefOID:     "same-sha",
		ReviewRequests: openReq(),
		Reviews:        []ghReview{{State: "APPROVED"}},
		CreatedAt:      fixedNow().Add(-10 * 24 * time.Hour),
	}
	if _, ok, _ := d.classify(context.Background(), "o/r", pr); ok {
		t.Error("PR with unchanged head SHA should not be Refreshed")
	}
}
