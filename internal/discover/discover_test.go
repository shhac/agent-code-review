package discover

import (
	"context"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
)

// fakeStore stubs the candidateStore consumer interface.
type fakeStore struct {
	last           store.Review
	hasLast        bool
	allowedAuthors map[string]bool // handle → allowed (for author-scoped repos)
}

func (f *fakeStore) UpsertCandidate(context.Context, store.Candidate) error { return nil }
func (f *fakeStore) LastReview(context.Context, string, int) (store.Review, bool, error) {
	return f.last, f.hasLast, nil
}
func (f *fakeStore) IsAuthorAllowed(_ context.Context, _ string, handle string) (bool, error) {
	return f.allowedAuthors[handle], nil
}

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

func TestClassifyApprovedRejected(t *testing.T) {
	d := newDiscoverer(&fakeStore{})
	pr := ghPR{
		Number:         4,
		ReviewRequests: openReq(),
		ReviewDecision: "APPROVED",
		CreatedAt:      fixedNow().Add(-2 * 24 * time.Hour),
	}
	if _, ok, _ := d.classify(context.Background(), "o/r", pr); ok {
		t.Error("a currently-approved PR is already unblocked and must not be a candidate")
	}
	// A STALE past approval (raw reviews list has APPROVED but the computed
	// decision doesn't) must NOT block — that's exactly the Refreshed case.
	stale := ghPR{
		Number:         5,
		HeadRefOID:     "new-sha",
		ReviewRequests: openReq(),
		Reviews:        []ghReview{{State: "APPROVED"}},
		ReviewDecision: "REVIEW_REQUIRED",
		CreatedAt:      fixedNow().Add(-2 * 24 * time.Hour),
	}
	fs := &fakeStore{hasLast: true, last: store.Review{HeadSHA: "old-sha"}}
	d2 := newDiscoverer(fs)
	if _, ok, _ := d2.classify(context.Background(), "o/r", stale); !ok {
		t.Error("stale approval must not block a Refreshed candidate")
	}
}

func TestClassifyAuthorScopedRepo(t *testing.T) {
	fs := &fakeStore{allowedAuthors: map[string]bool{"alice": true}}
	d := New(config.Config{
		Repos:                   []string{"o/scoped"},
		AllowedAuthorsOnlyRepos: []string{"o/scoped"},
	}, fs, nil)
	d.now = fixedNow

	pr := func(author string) ghPR {
		return ghPR{
			Number:         5,
			Author:         ghActor{Login: author},
			ReviewRequests: openReq(),
			CreatedAt:      fixedNow().Add(-24 * time.Hour),
		}
	}
	if _, ok, err := d.classify(context.Background(), "o/scoped", pr("alice")); err != nil || !ok {
		t.Errorf("allowed author must be discovered on a scoped repo (ok=%v err=%v)", ok, err)
	}
	if _, ok, _ := d.classify(context.Background(), "o/scoped", pr("mallory")); ok {
		t.Error("non-allowed author must be skipped on a scoped repo")
	}
	// Unscoped repo: anyone is discovered.
	unscoped := New(config.Config{Repos: []string{"o/open"}}, fs, nil)
	unscoped.now = fixedNow
	if _, ok, _ := unscoped.classify(context.Background(), "o/open", pr("mallory")); !ok {
		t.Error("unscoped repo must discover any open PR")
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
