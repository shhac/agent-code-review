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

// TestClassifyHolds pins the eligibility-hold computation: a freshly-updated
// PR gets a settling hold (quiet period), a recently-reviewed PR gets a
// cooldown hold, the later bound wins, and settled-and-cooled PRs carry no
// hold. Held PRs are still candidates — they enqueue as visible-but-not-yet-
// eligible rows rather than being silently dropped.
func TestClassifyHolds(t *testing.T) {
	newPR := func(updated time.Time) ghPR {
		return ghPR{
			Number:         8,
			HeadRefOID:     "sha8",
			CreatedAt:      fixedNow().Add(-2 * 24 * time.Hour),
			UpdatedAt:      updated,
			ReviewRequests: openReq(),
		}
	}

	t.Run("fresh update settles", func(t *testing.T) {
		d := newDiscoverer(&fakeStore{})
		c, ok, err := d.classify(context.Background(), d.cfg(), "o/r", newPR(fixedNow().Add(-5*time.Minute)))
		if err != nil || !ok {
			t.Fatalf("held PR must still classify as a candidate, ok=%v err=%v", ok, err)
		}
		want := fixedNow().Add(-5 * time.Minute).Add(15 * time.Minute) // updated + default quiet period
		if c.EligibleAt == nil || !c.EligibleAt.Equal(want) || c.HoldReason != store.HoldSettling {
			t.Errorf("eligible=%v reason=%q, want %v settling", c.EligibleAt, c.HoldReason, want)
		}
	})

	t.Run("quiet PR carries no hold", func(t *testing.T) {
		d := newDiscoverer(&fakeStore{})
		c, ok, _ := d.classify(context.Background(), d.cfg(), "o/r", newPR(fixedNow().Add(-time.Hour)))
		if !ok || c.EligibleAt != nil || c.HoldReason != "" {
			t.Errorf("settled PR must be eligible now: ok=%v eligible=%v reason=%q", ok, c.EligibleAt, c.HoldReason)
		}
	})

	t.Run("recent review cools down and outlasts settling", func(t *testing.T) {
		reviewedAt := fixedNow().Add(-30 * time.Minute)
		fs := &fakeStore{hasLast: true, last: store.Review{HeadSHA: "old-sha", Verdict: "COMMENTED", ReviewedAt: reviewedAt}}
		d := newDiscoverer(fs)
		pr := newPR(fixedNow().Add(-10 * time.Minute)) // settling would end sooner than the cooldown
		pr.Reviews = []ghReview{{State: "COMMENTED"}}  // not New → Refreshed
		c, ok, err := d.classify(context.Background(), d.cfg(), "o/r", pr)
		if err != nil || !ok {
			t.Fatalf("cooled-down PR must still classify, ok=%v err=%v", ok, err)
		}
		want := reviewedAt.Add(90 * time.Minute) // default rereview cooldown
		if c.EligibleAt == nil || !c.EligibleAt.Equal(want) || c.HoldReason != store.HoldCooldown {
			t.Errorf("eligible=%v reason=%q, want %v cooldown", c.EligibleAt, c.HoldReason, want)
		}
	})

	t.Run("disabled holds never fire", func(t *testing.T) {
		fs := &fakeStore{hasLast: true, last: store.Review{HeadSHA: "old-sha", Verdict: "COMMENTED", ReviewedAt: fixedNow().Add(-time.Minute)}}
		d := New(staticConfig(config.Config{
			Candidates: config.CandidateSettings{RereviewCooldown: "0s", QuietPeriod: "0s"},
		}), fs, nil)
		d.now = fixedNow
		pr := newPR(fixedNow()) // updated right now AND reviewed a minute ago
		pr.Reviews = []ghReview{{State: "COMMENTED"}}
		c, ok, _ := d.classify(context.Background(), d.cfg(), "o/r", pr)
		if !ok || c.EligibleAt != nil {
			t.Errorf("0s holds must disable: ok=%v eligible=%v", ok, c.EligibleAt)
		}
	})
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
