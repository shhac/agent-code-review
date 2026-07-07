package discover

import (
	"context"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
)

// fakeStore stubs the candidateStore consumer interface. `last` is the most
// recent REAL review; `outcome` the most recent row of any verdict — when
// unset it falls back to `last`, mirroring the real store (a real review is
// also the latest outcome unless a skip/error came after it).
type fakeStore struct {
	last           store.Review
	hasLast        bool
	outcome        store.Review
	hasOutcome     bool
	allowedAuthors map[string]bool // handle → allowed (for author-scoped repos)
}

func (f *fakeStore) Enqueue(context.Context, store.Candidate) error { return nil }
func (f *fakeStore) LastReview(context.Context, string, int) (store.Review, bool, error) {
	return f.last, f.hasLast, nil
}
func (f *fakeStore) LastOutcome(context.Context, string, int) (store.Review, bool, error) {
	if f.hasOutcome {
		return f.outcome, true, nil
	}
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

// Same-SHA suppression: any outcome at the PR's current head means nothing to
// do — skips and errors don't thrash, and an engine-reported review that gh
// hasn't surfaced yet can't re-enqueue in a loop. New commits re-enqueue.
func TestClassifySameSHASuppression(t *testing.T) {
	pr := func(sha string) ghPR {
		return ghPR{
			Number:         3,
			HeadRefOID:     sha,
			ReviewRequests: openReq(),
			CreatedAt:      fixedNow().Add(-2 * 24 * time.Hour),
		}
	}
	for _, verdict := range []string{"SKIPPED", "ERROR", "APPROVED"} {
		fs := &fakeStore{hasOutcome: true, outcome: store.Review{HeadSHA: "sha1", Verdict: verdict}}
		d := newDiscoverer(fs)
		if _, ok, _ := d.classify(context.Background(), "o/r", pr("sha1")); ok {
			t.Errorf("%s outcome at the current SHA must suppress re-enqueue", verdict)
		}
	}
	// New commits after a skip: outcome SHA differs → eligible again (as New;
	// no real review exists and the PR has no gh reviews).
	fs := &fakeStore{hasOutcome: true, outcome: store.Review{HeadSHA: "sha1", Verdict: "SKIPPED"}}
	d := newDiscoverer(fs)
	c, ok, err := d.classify(context.Background(), "o/r", pr("sha2"))
	if err != nil || !ok {
		t.Fatalf("skipped PR with new commits must re-enqueue, ok=%v err=%v", ok, err)
	}
	if c.Type != store.TypeNew {
		t.Errorf("type = %q, want new (no real review recorded)", c.Type)
	}
}

// The bug that motivated the queue/history split: a PR we reviewed at an old
// SHA gets new commits — it must come back as a Refreshed candidate.
func TestClassifyRefreshedAfterNewCommits(t *testing.T) {
	fs := &fakeStore{
		hasLast:    true,
		last:       store.Review{HeadSHA: "old-sha", Verdict: "COMMENTED"},
		hasOutcome: true,
		outcome:    store.Review{HeadSHA: "old-sha", Verdict: "COMMENTED"},
	}
	d := newDiscoverer(fs)
	pr := ghPR{
		Number:         6,
		HeadRefOID:     "new-sha",
		ReviewRequests: openReq(),
		Reviews:        []ghReview{{State: "COMMENTED"}},
		CreatedAt:      fixedNow().Add(-5 * 24 * time.Hour),
	}
	c, ok, err := d.classify(context.Background(), "o/r", pr)
	if err != nil || !ok {
		t.Fatalf("expected Refreshed candidate after new commits, ok=%v err=%v", ok, err)
	}
	if c.Type != store.TypeRefreshed {
		t.Errorf("type = %q, want refreshed", c.Type)
	}
}
