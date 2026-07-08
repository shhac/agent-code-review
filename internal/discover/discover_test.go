package discover

import (
	"context"
	"errors"
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
	enqueued       []store.Candidate
}

func (f *fakeStore) Enqueue(_ context.Context, c store.Candidate) error {
	f.enqueued = append(f.enqueued, c)
	return nil
}
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

func staticConfig(c config.Config) func() config.Config {
	return func() config.Config { return c }
}

func newDiscoverer(fs *fakeStore) *Discoverer {
	d := New(staticConfig(config.Config{}), fs, nil)
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
	c, ok, err := d.classify(context.Background(), d.cfg(), "o/r", pr)
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
	if _, ok, _ := d.classify(context.Background(), d.cfg(), "o/r", pr); ok {
		t.Error("draft PR should not be a candidate")
	}
}

func TestClassifyNoReviewRequestRejected(t *testing.T) {
	d := newDiscoverer(&fakeStore{})
	pr := ghPR{Number: 1, CreatedAt: fixedNow()} // no review requested
	if _, ok, _ := d.classify(context.Background(), d.cfg(), "o/r", pr); ok {
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
	if _, ok, _ := d.classify(context.Background(), d.cfg(), "o/r", pr); ok {
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
	c, ok, err := d.classify(context.Background(), d.cfg(), "o/r", pr)
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
	if _, ok, _ := d.classify(context.Background(), d.cfg(), "o/r", pr); ok {
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
	if _, ok, _ := d2.classify(context.Background(), d2.cfg(), "o/r", stale); !ok {
		t.Error("stale approval must not block a Refreshed candidate")
	}
}

func TestClassifyAuthorScopedRepo(t *testing.T) {
	fs := &fakeStore{allowedAuthors: map[string]bool{"alice": true}}
	d := New(staticConfig(config.Config{
		Repos:                   []string{"o/scoped"},
		AllowedAuthorsOnlyRepos: []string{"o/scoped"},
	}), fs, nil)
	d.now = fixedNow

	pr := func(author string) ghPR {
		return ghPR{
			Number:         5,
			Author:         ghActor{Login: author},
			ReviewRequests: openReq(),
			CreatedAt:      fixedNow().Add(-24 * time.Hour),
		}
	}
	if _, ok, err := d.classify(context.Background(), d.cfg(), "o/scoped", pr("alice")); err != nil || !ok {
		t.Errorf("allowed author must be discovered on a scoped repo (ok=%v err=%v)", ok, err)
	}
	if _, ok, _ := d.classify(context.Background(), d.cfg(), "o/scoped", pr("mallory")); ok {
		t.Error("non-allowed author must be skipped on a scoped repo")
	}
	// Unscoped repo: anyone is discovered.
	unscoped := New(staticConfig(config.Config{Repos: []string{"o/open"}}), fs, nil)
	unscoped.now = fixedNow
	if _, ok, _ := unscoped.classify(context.Background(), unscoped.cfg(), "o/open", pr("mallory")); !ok {
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
	if _, ok, _ := d.classify(context.Background(), d.cfg(), "o/r", pr); ok {
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
		if _, ok, _ := d.classify(context.Background(), d.cfg(), "o/r", pr("sha1")); ok {
			t.Errorf("%s outcome at the current SHA must suppress re-enqueue", verdict)
		}
	}
	// New commits after a skip: outcome SHA differs → eligible again (as New;
	// no real review exists and the PR has no gh reviews).
	fs := &fakeStore{hasOutcome: true, outcome: store.Review{HeadSHA: "sha1", Verdict: "SKIPPED"}}
	d := newDiscoverer(fs)
	c, ok, err := d.classify(context.Background(), d.cfg(), "o/r", pr("sha2"))
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
	c, ok, err := d.classify(context.Background(), d.cfg(), "o/r", pr)
	if err != nil || !ok {
		t.Fatalf("expected Refreshed candidate after new commits, ok=%v err=%v", ok, err)
	}
	if c.Type != store.TypeRefreshed {
		t.Errorf("type = %q, want refreshed", c.Type)
	}
}

// TestDiscoverSweep pins the sweep's per-repo resilience: one failing repo is
// logged and skipped so it can't take down the cycle, matches from healthy
// repos are enqueued, and an error surfaces only when EVERY repo failed
// (which usually means gh itself is broken).
func TestDiscoverSweep(t *testing.T) {
	errGH := errors.New("gh: boom")
	newPR := ghPR{
		Number:         7,
		HeadRefOID:     "sha7",
		CreatedAt:      fixedNow().Add(-24 * time.Hour),
		ReviewRequests: openReq(),
	}
	sweep := func(t *testing.T, listPRs func(context.Context, string) ([]ghPR, error)) (*fakeStore, []store.Candidate, error) {
		t.Helper()
		fs := &fakeStore{}
		d := New(staticConfig(config.Config{Repos: []string{"o/broken", "o/healthy"}}), fs, nil)
		d.now = fixedNow
		d.listPRs = listPRs
		found, err := d.Discover(context.Background())
		return fs, found, err
	}

	t.Run("one failing repo is skipped", func(t *testing.T) {
		fs, found, err := sweep(t, func(_ context.Context, repo string) ([]ghPR, error) {
			if repo == "o/broken" {
				return nil, errGH
			}
			return []ghPR{newPR}, nil
		})
		if err != nil {
			t.Fatalf("partial failure must not error, got %v", err)
		}
		if len(found) != 1 || found[0].Repo != "o/healthy" || found[0].Number != 7 {
			t.Errorf("healthy repo's candidate must survive, got %+v", found)
		}
		if len(fs.enqueued) != 1 {
			t.Errorf("candidate must be enqueued exactly once, got %d", len(fs.enqueued))
		}
	})

	t.Run("all repos failing errors", func(t *testing.T) {
		_, _, err := sweep(t, func(context.Context, string) ([]ghPR, error) {
			return nil, errGH
		})
		if err == nil || !errors.Is(err, errGH) {
			t.Errorf("total failure must surface the gh error, got %v", err)
		}
	})

	t.Run("non-candidates are not enqueued", func(t *testing.T) {
		draft := newPR
		draft.IsDraft = true
		fs, found, err := sweep(t, func(context.Context, string) ([]ghPR, error) {
			return []ghPR{draft}, nil
		})
		if err != nil || len(found) != 0 || len(fs.enqueued) != 0 {
			t.Errorf("draft PRs must classify out, got found=%v enqueued=%v err=%v", found, fs.enqueued, err)
		}
	})
}
