package discover

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
)

// fakeStore stubs the candidateStore consumer interface. `last` is the most
// recent REAL review; `outcome` the most recent row of any verdict; when
// unset it falls back to `last`, mirroring the real store (a real review is
// also the latest outcome unless a skip/error came after it).
type fakeStore struct {
	last         store.Review
	hasLast      bool
	outcome      store.Review
	hasOutcome   bool
	authorGroups map[string]string // handle → group; absent = unlisted
	authorErr    error             // simulate the roster lookup failing
	enqueued     []store.Candidate
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
func (f *fakeStore) AuthorGroup(_ context.Context, _ string, handle string) (config.Membership, error) {
	if f.authorErr != nil {
		return config.Membership{}, f.authorErr
	}
	group, ok := f.authorGroups[handle]
	if !ok {
		return config.Membership{}, nil
	}
	return config.Membership{Group: group, Repo: config.WildcardRepo}, nil
}

func fixedNow() time.Time { return time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC) }

func staticConfig(c config.Config) func() config.Config {
	return func() config.Config { return c }
}

func newDiscoverer(fs *fakeStore) *Discoverer {
	d := New(staticConfig(config.Config{}), fs, nil)
	d.now = fixedNow
	// Default to "nobody has said anything", so only the tests that care about
	// Discussion detection pay attention to it and none of them touch gh.
	d.lastHumanActivity = func(context.Context, string, int) (time.Time, error) {
		return time.Time{}, nil
	}
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
	// decision doesn't) must NOT block: that's exactly the Refreshed case.
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

func samplePR(author string) ghPR {
	return ghPR{
		Number:         5,
		Author:         ghActor{Login: author},
		ReviewRequests: openReq(),
		CreatedAt:      fixedNow().Add(-24 * time.Hour),
	}
}

// A repo listed in allowed_authors_only_repos keeps its exact pre-groups
// meaning: unlisted authors are not discovered there, and are on every other
// repo. Nobody has to adopt authors.unlisted to keep what they had.
func TestClassifyAuthorScopedRepoLegacyConfig(t *testing.T) {
	fs := &fakeStore{authorGroups: map[string]string{"alice": config.GroupApprover}}
	d := New(staticConfig(config.Config{
		Repos:                   []string{"o/scoped"},
		AllowedAuthorsOnlyRepos: []string{"o/scoped"},
	}), fs, nil)
	d.now = fixedNow

	if _, ok, err := d.classify(context.Background(), d.cfg(), "o/scoped", samplePR("alice")); err != nil || !ok {
		t.Errorf("rostered author must be discovered on a scoped repo (ok=%v err=%v)", ok, err)
	}
	if _, ok, _ := d.classify(context.Background(), d.cfg(), "o/scoped", samplePR("mallory")); ok {
		t.Error("unlisted author must be skipped on a scoped repo")
	}
	// Unscoped repo: anyone is discovered.
	unscoped := New(staticConfig(config.Config{Repos: []string{"o/open"}}), fs, nil)
	unscoped.now = fixedNow
	if _, ok, _ := unscoped.classify(context.Background(), unscoped.cfg(), "o/open", samplePR("mallory")); !ok {
		t.Error("unscoped repo must discover any open PR")
	}
}

// The group ladder replaces that repo list: an "ignore" policy is what stops
// discovery now, and it can come from the author's own group rather than only
// from the repo they filed against.
func TestClassifyGatesOnTheIgnoreLevel(t *testing.T) {
	cfg := config.Config{
		Repos: []string{"o/repo"},
		Authors: config.AuthorSettings{
			Unlisted: map[string]string{"*": "outsider"},
			Groups: map[string]config.Group{
				"outsider": {Review: config.ReviewComment},
				"bots":     {Review: config.ReviewIgnore},
			},
		},
	}
	fs := &fakeStore{authorGroups: map[string]string{"dependabot": "bots", "alice": "outsider"}}
	d := New(staticConfig(cfg), fs, nil)
	d.now = fixedNow

	if _, ok, _ := d.classify(context.Background(), cfg, "o/repo", samplePR("dependabot")); ok {
		t.Error("an ignore-level author must not be discovered")
	}
	if _, ok, _ := d.classify(context.Background(), cfg, "o/repo", samplePR("alice")); !ok {
		t.Error("a comment-level author must still be discovered")
	}
	// An unlisted author lands on the unlisted group, which reviews.
	if _, ok, _ := d.classify(context.Background(), cfg, "o/repo", samplePR("stranger")); !ok {
		t.Error("unlisted authors must follow authors.unlisted, which reviews here")
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
// do; skips and errors don't thrash, and an engine-reported review that gh
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
// SHA gets new commits; it must come back as a Refreshed candidate.
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

// TestClassifyType table-tests the pure New/Refreshed/Discussion decision at
// its boundaries; no fakes needed, which is why it was extracted.
func TestClassifyType(t *testing.T) {
	now := fixedNow()
	cfg := config.Config{} // defaults: New ≤ 14d, Refreshed ≤ 21d
	day := 24 * time.Hour
	cases := []struct {
		name       string
		pr         ghPR
		last       store.Review
		reviewed   bool
		discussion bool
		wantType   string
		wantOK     bool
	}{
		{"fresh unreviewed is New", ghPR{CreatedAt: now.Add(-day)}, store.Review{}, false, false, store.TypeNew, true},
		{"exactly at the New window edge is New", ghPR{CreatedAt: now.Add(-14 * day)}, store.Review{}, false, false, store.TypeNew, true},
		{"past the New window, never reviewed by us: neither", ghPR{CreatedAt: now.Add(-15 * day)}, store.Review{}, false, false, "", false},
		{"gh review exists but not ours: not New, not Refreshed", ghPR{CreatedAt: now.Add(-day), Reviews: []ghReview{{State: "COMMENTED"}}}, store.Review{}, false, false, "", false},
		{"ours at a different SHA is Refreshed", ghPR{CreatedAt: now.Add(-15 * day), HeadRefOID: "b", Reviews: []ghReview{{State: "COMMENTED"}}}, store.Review{HeadSHA: "a"}, true, false, store.TypeRefreshed, true},
		{"ours at the same SHA, quiet: neither", ghPR{CreatedAt: now.Add(-day), HeadRefOID: "a", Reviews: []ghReview{{State: "COMMENTED"}}}, store.Review{HeadSHA: "a"}, true, false, "", false},
		{"ours at the same SHA with new conversation is Discussion", ghPR{CreatedAt: now.Add(-day), HeadRefOID: "a", Reviews: []ghReview{{State: "COMMENTED"}}}, store.Review{HeadSHA: "a"}, true, true, store.TypeDiscussion, true},
		{"Discussion wins over New: a replied-to PR is not a first look", ghPR{CreatedAt: now.Add(-day)}, store.Review{}, true, true, store.TypeDiscussion, true},
		{"past the Refreshed window: neither", ghPR{CreatedAt: now.Add(-22 * day), HeadRefOID: "b", Reviews: []ghReview{{State: "COMMENTED"}}}, store.Review{HeadSHA: "a"}, true, false, "", false},
		{"New wins when both could match", ghPR{CreatedAt: now.Add(-day), HeadRefOID: "b"}, store.Review{HeadSHA: "a"}, true, false, store.TypeNew, true},
	}
	for _, tc := range cases {
		typ, ok := classifyType(tc.pr, cfg, now, tc.last, tc.reviewed, tc.discussion)
		if typ != tc.wantType || ok != tc.wantOK {
			t.Errorf("%s: classifyType = (%q, %v), want (%q, %v)", tc.name, typ, ok, tc.wantType, tc.wantOK)
		}
	}
}

// TestClassifyHolds pins the eligibility-hold computation: a freshly-updated
// PR gets a settling hold (quiet period), a recently-reviewed PR gets a
// cooldown hold, the later bound wins, and settled-and-cooled PRs carry no
// hold. Held PRs are still candidates; they enqueue as visible-but-not-yet-
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

	// ready reports what EffectiveReady makes of the holds a classify produced:
	// the instant the row becomes reviewable and the name deciding it.
	ready := func(c store.Candidate) (time.Time, string) { return c.EffectiveReady() }

	t.Run("fresh update settles", func(t *testing.T) {
		d := newDiscoverer(&fakeStore{})
		c, ok, err := d.classify(context.Background(), d.cfg(), "o/r", newPR(fixedNow().Add(-5*time.Minute)))
		if err != nil || !ok {
			t.Fatalf("held PR must still classify as a candidate, ok=%v err=%v", ok, err)
		}
		want := fixedNow().Add(-5 * time.Minute).Add(15 * time.Minute) // updated + default quiet period
		if until, reason := ready(c); !until.Equal(want) || reason != store.HoldSettling {
			t.Errorf("ready=%v reason=%q, want %v settling", until, reason, want)
		}
	})

	t.Run("quiet PR carries no hold", func(t *testing.T) {
		d := newDiscoverer(&fakeStore{})
		c, ok, _ := d.classify(context.Background(), d.cfg(), "o/r", newPR(fixedNow().Add(-time.Hour)))
		if !ok || len(c.Holds) != 0 {
			t.Errorf("settled PR must be eligible now: ok=%v holds=%v", ok, c.Holds)
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
		if until, reason := ready(c); !until.Equal(want) || reason != store.HoldCooldown {
			t.Errorf("ready=%v reason=%q, want %v cooldown", until, reason, want)
		}
	})

	t.Run("fresh push during cooldown keeps both holds; the later wins", func(t *testing.T) {
		reviewedAt := fixedNow().Add(-80 * time.Minute) // cooldown ends in 10m
		fs := &fakeStore{hasLast: true, last: store.Review{HeadSHA: "old-sha", Verdict: "COMMENTED", ReviewedAt: reviewedAt}}
		d := newDiscoverer(fs)
		pr := newPR(fixedNow().Add(-time.Minute)) // settling ends in 14m, later than the cooldown
		pr.Reviews = []ghReview{{State: "COMMENTED"}}
		c, ok, err := d.classify(context.Background(), d.cfg(), "o/r", pr)
		if err != nil || !ok {
			t.Fatalf("held PR must still classify, ok=%v err=%v", ok, err)
		}
		// Both are recorded: the map keeps every hold, and only the reader
		// decides which one is operative. The old single column could not say
		// this, which is what let a release of one lift the other.
		if len(c.Holds) != 2 {
			t.Fatalf("holds = %v, want both settling and cooldown", c.Holds)
		}
		if got, want := c.Holds[store.HoldCooldown], reviewedAt.Add(90*time.Minute); !got.Equal(want) {
			t.Errorf("cooldown hold = %v, want %v", got, want)
		}
		want := fixedNow().Add(-time.Minute).Add(15 * time.Minute)
		if until, reason := ready(c); !until.Equal(want) || reason != store.HoldSettling {
			t.Errorf("ready=%v reason=%q, want %v settling (the later bound must win)", until, reason, want)
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
		if !ok || len(c.Holds) != 0 {
			t.Errorf("0s holds must disable: ok=%v holds=%v", ok, c.Holds)
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

// An unreadable roster row must abort the sweep rather than fall through to a
// guessed policy: guessing here would enqueue (or silently drop) PRs on a
// policy nobody wrote. Discovery fails closed, and the next sweep retries.
func TestClassifyPropagatesRosterLookupError(t *testing.T) {
	fs := &fakeStore{authorErr: errors.New("store unavailable")}
	d := New(staticConfig(config.Config{Repos: []string{"o/repo"}}), fs, nil)
	d.now = fixedNow

	_, ok, err := d.classify(context.Background(), d.cfg(), "o/repo", samplePR("alice"))
	if err == nil {
		t.Fatal("a roster lookup error must propagate")
	}
	if ok {
		t.Error("no candidate may be classified when the policy could not be resolved")
	}
	if len(fs.enqueued) != 0 {
		t.Errorf("nothing may be enqueued, got %+v", fs.enqueued)
	}
}

// reviewedAt is our own recorded review time in the Discussion tests: the PR
// exists, we reviewed it, and everything below varies what happened after.
func reviewedAt() time.Time { return fixedNow().Add(-2 * time.Hour) }

// sameSHAReviewed is the setup every Discussion test starts from: a real review
// of ours recorded at the PR's CURRENT head SHA. Before Discussion detection
// this shape was suppressed unconditionally.
func sameSHAReviewed() *fakeStore {
	return &fakeStore{
		hasLast: true,
		last:    store.Review{HeadSHA: "sha1", ReviewedAt: reviewedAt()},
	}
}

func sameSHAPR() ghPR {
	return ghPR{
		Number:         7,
		HeadRefOID:     "sha1",
		ReviewRequests: openReq(),
		Reviews:        []ghReview{{State: "COMMENTED"}},
		CreatedAt:      fixedNow().Add(-3 * 24 * time.Hour),
		UpdatedAt:      fixedNow().Add(-1 * time.Hour), // touched since our review
	}
}

// TestClassifyDiscussionOnReplyAtSameSHA is the regression this whole feature
// exists for: the author replies to a finding without pushing, and we must not
// treat the unchanged head SHA as "nothing to do".
func TestClassifyDiscussionOnReplyAtSameSHA(t *testing.T) {
	d := newDiscoverer(sameSHAReviewed())
	d.lastHumanActivity = func(context.Context, string, int) (time.Time, error) {
		return fixedNow().Add(-1 * time.Hour), nil // after our review
	}
	c, ok, err := d.classify(context.Background(), d.cfg(), "o/r", sameSHAPR())
	if err != nil || !ok {
		t.Fatalf("expected a Discussion candidate, ok=%v err=%v", ok, err)
	}
	if c.Type != store.TypeDiscussion {
		t.Errorf("type = %q, want discussion", c.Type)
	}
}

// TestClassifySuppressesConversationAlreadyActedOn is the loop regression.
//
// The conversation watermark has to move every time we LOOK, not only when we
// post a real review. It did not, so a single human reply made the same-SHA
// escape hatch true forever: the PR was re-enqueued, rechecked and re-skipped
// on every sweep, indefinitely. In the live store one stack of six PRs had
// burned ~300 cycles each over five days, and precheck skips had grown to 45%
// of all recorded history.
func TestClassifySuppressesConversationAlreadyActedOn(t *testing.T) {
	// A real review, then a later skip: exactly the shape the loop produced.
	// The human spoke after the review but before the skip, so we have already
	// acted on everything there is to act on.
	fs := sameSHAReviewed()
	fs.hasOutcome = true
	fs.outcome = store.Review{HeadSHA: "sha1", Verdict: store.VerdictSkipped, ReviewedAt: fixedNow().Add(-30 * time.Minute)}

	d := newDiscoverer(fs)
	d.lastHumanActivity = func(context.Context, string, int) (time.Time, error) {
		return fixedNow().Add(-1 * time.Hour), nil // after the review, before the skip
	}
	if _, ok, _ := d.classify(context.Background(), d.cfg(), "o/r", sameSHAPR()); ok {
		t.Error("conversation we have already looked at must not re-enqueue; this is the loop")
	}

	// And the feature still works: a reply arriving after the LAST look is new
	// information, whatever the last look concluded. updated_at moves with it,
	// because GitHub bumps the PR when somebody comments — the cheap gate ahead
	// of the probe relies on exactly that.
	d.lastHumanActivity = func(context.Context, string, int) (time.Time, error) {
		return fixedNow().Add(-10 * time.Minute), nil // after the skip
	}
	fresh := sameSHAPR()
	fresh.UpdatedAt = fixedNow().Add(-10 * time.Minute)
	c, ok, err := d.classify(context.Background(), d.cfg(), "o/r", fresh)
	if err != nil || !ok {
		t.Fatalf("a reply since the last look must still be a Discussion candidate, ok=%v err=%v", ok, err)
	}
	if c.Type != store.TypeDiscussion {
		t.Errorf("type = %q, want discussion", c.Type)
	}
}

// TestClassifyQuietSameSHAStillSuppressed pins the other half: without new
// conversation, an unchanged SHA must stay suppressed exactly as before, or
// every sweep would re-enqueue every PR we have ever reviewed.
func TestClassifyQuietSameSHAStillSuppressed(t *testing.T) {
	d := newDiscoverer(sameSHAReviewed())
	d.lastHumanActivity = func(context.Context, string, int) (time.Time, error) {
		return fixedNow().Add(-5 * time.Hour), nil // predates our review
	}
	if _, ok, _ := d.classify(context.Background(), d.cfg(), "o/r", sameSHAPR()); ok {
		t.Error("same SHA with no conversation since our review must stay suppressed")
	}
}

// TestClassifyDiscussionSkipsProbeWhenUntouched pins the cheap gate: gh's
// updatedAt is a necessary condition for new conversation, so a PR nobody has
// touched since our review must not cost an API call.
func TestClassifyDiscussionSkipsProbeWhenUntouched(t *testing.T) {
	d := newDiscoverer(sameSHAReviewed())
	probed := false
	d.lastHumanActivity = func(context.Context, string, int) (time.Time, error) {
		probed = true
		return fixedNow(), nil
	}
	pr := sameSHAPR()
	pr.UpdatedAt = reviewedAt().Add(-time.Minute) // untouched since our review
	if _, ok, _ := d.classify(context.Background(), d.cfg(), "o/r", pr); ok {
		t.Error("untouched PR should not be a candidate")
	}
	if probed {
		t.Error("probe ran for a PR gh says nobody has touched since our review")
	}
}

// TestClassifyDiscussionNeedsPriorRealReview: a targeted re-review re-judges a
// previous review's findings, so without one there is nothing to revisit. Only
// a skip or error is on record here, not a verdict.
func TestClassifyDiscussionNeedsPriorRealReview(t *testing.T) {
	fs := &fakeStore{hasOutcome: true, outcome: store.Review{HeadSHA: "sha1", ReviewedAt: reviewedAt()}}
	d := newDiscoverer(fs)
	probed := false
	d.lastHumanActivity = func(context.Context, string, int) (time.Time, error) {
		probed = true
		return fixedNow(), nil
	}
	if _, ok, _ := d.classify(context.Background(), d.cfg(), "o/r", sameSHAPR()); ok {
		t.Error("no real review in history: nothing to revisit, so not a candidate")
	}
	if probed {
		t.Error("probe ran without a prior real review to re-judge")
	}
}

// TestClassifyDiscussionRespectsMaxAge: conversation on an old PR is usually
// about landing it, not about the review.
func TestClassifyDiscussionRespectsMaxAge(t *testing.T) {
	d := newDiscoverer(sameSHAReviewed())
	d.lastHumanActivity = func(context.Context, string, int) (time.Time, error) {
		return fixedNow().Add(-1 * time.Hour), nil
	}
	pr := sameSHAPR()
	pr.CreatedAt = fixedNow().Add(-15 * 24 * time.Hour) // 15d > 14d Discussion window
	if _, ok, _ := d.classify(context.Background(), d.cfg(), "o/r", pr); ok {
		t.Error("PR older than the Discussion window should not be a Discussion candidate")
	}
}

// TestClassifyDiscussionFailsClosed: discovery sweeps run on a short cadence,
// so a probe that errors every time must not re-enqueue the PR every time.
func TestClassifyDiscussionFailsClosed(t *testing.T) {
	d := newDiscoverer(sameSHAReviewed())
	d.lastHumanActivity = func(context.Context, string, int) (time.Time, error) {
		return time.Time{}, errors.New("gh exploded")
	}
	if _, ok, _ := d.classify(context.Background(), d.cfg(), "o/r", sameSHAPR()); ok {
		t.Error("probe failure must suppress, not enqueue")
	}
}

// TestLatestHumanActivity pins the two exclusions the Discussion trigger rests
// on. Bots comment constantly and none of it is a reason to look again; our own
// review is not somebody responding to our review.
func TestLatestHumanActivity(t *testing.T) {
	base := fixedNow()
	at := func(d time.Duration, login, typename string) ghActivityNode {
		return ghActivityNode{CreatedAt: base.Add(d), Author: ghGraphQLActor{Login: login, Typename: typename}}
	}
	var resp ghActivityResp
	pr := &resp.Data.Repository.PullRequest
	pr.Comments.Nodes = []ghActivityNode{at(-4*time.Hour, "human", "User")}
	pr.Reviews.Nodes = []ghActivityNode{
		at(-1*time.Minute, "us", "User"),       // our own review: excluded
		at(-2*time.Minute, "deploybot", "Bot"), // bot: excluded
	}
	got := latestHumanActivity(resp, "us")
	if want := base.Add(-4 * time.Hour); !got.Equal(want) {
		t.Errorf("latestHumanActivity = %v, want %v (the human comment, not our review or the bot)", got, want)
	}

	// A reply in a thread is the case the old fingerprint could not see at all.
	var thread ghActivityThread
	thread.Comments.Nodes = []ghActivityNode{at(-30*time.Minute, "human", "User")}
	pr.ReviewThreads.Nodes = append(pr.ReviewThreads.Nodes, thread)
	if got, want := latestHumanActivity(resp, "us"), base.Add(-30*time.Minute); !got.Equal(want) {
		t.Errorf("latestHumanActivity = %v, want the thread reply at %v", got, want)
	}
}

// TestLatestHumanActivityEmpty: nobody has said anything, which must read as
// the zero time rather than "now".
func TestLatestHumanActivityEmpty(t *testing.T) {
	if got := latestHumanActivity(ghActivityResp{}, "us"); !got.IsZero() {
		t.Errorf("latestHumanActivity on an empty response = %v, want zero", got)
	}
}

// --- sweep resilience: retry, cross-cycle backoff, and the budget ---

// sweepHarness drives repeated sweeps over a fake gh with a controllable
// clock, recording which repos were actually listed on each one. Every test
// below asks the same question: what does a sweep COST when GitHub is
// misbehaving, measured in calls we did not have to make.
type sweepHarness struct {
	d       *Discoverer
	now     time.Time
	calls   []string // repo per listPRs call, across all sweeps
	failing map[string]bool
	warns   []string
}

func newSweepHarness(t *testing.T, repos []string) *sweepHarness {
	t.Helper()
	h := &sweepHarness{now: fixedNow(), failing: map[string]bool{}}
	cfg := config.Config{Repos: repos}
	cfg.Discovery.Interval = "5m"
	h.d = New(staticConfig(cfg), &fakeStore{}, nil)
	h.d.now = func() time.Time { return h.now }
	h.d.warnf = func(format string, args ...any) {
		h.warns = append(h.warns, fmt.Sprintf(format, args...))
	}
	h.d.listPRs = func(_ context.Context, repo string) ([]ghPR, error) {
		h.calls = append(h.calls, repo)
		if h.failing[repo] {
			return nil, errors.New("gh pr list: HTTP 502: 502 Bad Gateway")
		}
		return nil, nil
	}
	return h
}

func (h *sweepHarness) sweep(t *testing.T) error {
	t.Helper()
	_, err := h.d.Discover(context.Background())
	return err
}

func (h *sweepHarness) callsSince(n int) []string { return h.calls[n:] }

// A failing repo must not be retried on the very next cycle: that is the
// cascade, one 502 becoming a 502 every five minutes forever.
func TestSweepBackoffSkipsNextCycle(t *testing.T) {
	h := newSweepHarness(t, []string{"o/a", "o/b"})
	h.failing["o/a"] = true

	if err := h.sweep(t); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if got := len(h.calls); got != 2 {
		t.Fatalf("first sweep made %d calls, want 2 (both repos tried)", got)
	}

	// One minute later, inside o/a's 2m window: it must be skipped, and o/b
	// must be unaffected by its neighbour's failure.
	h.now = h.now.Add(1 * time.Minute)
	mark := len(h.calls)
	if err := h.sweep(t); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if got := h.callsSince(mark); len(got) != 1 || got[0] != "o/b" {
		t.Fatalf("second sweep listed %v, want [o/b] only (o/a still in backoff)", got)
	}
	if len(h.warns) == 0 || !strings.Contains(h.warns[len(h.warns)-1], "in backoff") {
		t.Fatalf("expected a backoff log line, got %v", h.warns)
	}
}

// Past the window the repo is tried again, and a success clears the count so
// the next failure starts from the short end of the curve rather than the long.
func TestSweepBackoffExpiresAndRecovers(t *testing.T) {
	h := newSweepHarness(t, []string{"o/a"})
	h.failing["o/a"] = true
	_ = h.sweep(t) // failure 1 → 2m

	h.now = h.now.Add(3 * time.Minute)
	h.failing["o/a"] = false
	mark := len(h.calls)
	if err := h.sweep(t); err != nil {
		t.Fatalf("sweep after window: %v", err)
	}
	if got := h.callsSince(mark); len(got) != 1 {
		t.Fatalf("expected o/a retried once the window passed, got %v", got)
	}

	// Recovered: a fresh failure is failure 1 again (2m), not failure 2 (4m).
	h.failing["o/a"] = true
	h.now = h.now.Add(time.Minute)
	_ = h.sweep(t)
	if _, fails, held := h.d.inBackoff("o/a", h.now); !held || fails != 1 {
		t.Fatalf("after recovery, failure count = %d (held=%v), want 1", fails, held)
	}
}

// Consecutive failures lengthen the window, capped so a dead repo still gets
// retried twice an hour rather than never.
func TestBackoffCurve(t *testing.T) {
	for _, tc := range []struct {
		failures int
		want     time.Duration
	}{
		{1, 2 * time.Minute},
		{2, 4 * time.Minute},
		{3, 8 * time.Minute},
		{4, 16 * time.Minute},
		{5, 30 * time.Minute},
		{50, 30 * time.Minute},
	} {
		if got := backoffFor(tc.failures); got != tc.want {
			t.Errorf("backoffFor(%d) = %s, want %s", tc.failures, got, tc.want)
		}
	}
}

// Repos skipped because they are already backed off must not count as fresh
// failures, or one outage becomes a hard error on every subsequent cycle.
func TestAllReposBackedOffIsNotAnError(t *testing.T) {
	h := newSweepHarness(t, []string{"o/a", "o/b"})
	h.failing["o/a"], h.failing["o/b"] = true, true

	if err := h.sweep(t); err == nil {
		t.Fatal("first sweep: want an error when every repo fails, got nil")
	}
	h.now = h.now.Add(time.Minute)
	if err := h.sweep(t); err != nil {
		t.Fatalf("second sweep: want nil while both repos wait out backoff, got %v", err)
	}
}

// A sweep that runs out of budget resumes where it stopped, so the tail of the
// repo list is delayed by a cycle rather than starved forever.
func TestSweepBudgetResumesAtUnreachedRepo(t *testing.T) {
	h := newSweepHarness(t, []string{"o/a", "o/b", "o/c"})
	// Each listing burns four minutes of a five-minute budget, so the first
	// sweep gets through o/a and then finds itself over the line.
	inner := h.d.listPRs
	h.d.listPRs = func(ctx context.Context, repo string) ([]ghPR, error) {
		h.now = h.now.Add(4 * time.Minute)
		return inner(ctx, repo)
	}

	if err := h.sweep(t); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if got := h.calls; len(got) != 2 || got[0] != "o/a" || got[1] != "o/b" {
		t.Fatalf("first sweep listed %v, want [o/a o/b] before the budget ran out", got)
	}
	if len(h.warns) == 0 || !strings.Contains(h.warns[len(h.warns)-1], "sweep budget") {
		t.Fatalf("expected a budget log line, got %v", h.warns)
	}

	mark := len(h.calls)
	if err := h.sweep(t); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if got := h.callsSince(mark); len(got) == 0 || got[0] != "o/c" {
		t.Fatalf("second sweep started at %v, want o/c first (the repo the budget cut off)", got)
	}
}

// --- page-size ladder ---

func TestListLimitsHalvesToAFloor(t *testing.T) {
	for _, tc := range []struct {
		full int
		want []int
	}{
		{300, []int{300, 150, 75}},
		{100, []int{100, 50, 25}},
		{60, []int{60, 30, 25}},
		// At or below the floor there is nothing to give up: one attempt,
		// not three identical ones.
		{25, []int{25}},
		{10, []int{10}},
	} {
		got := listLimits(tc.full)
		if len(got) != len(tc.want) {
			t.Errorf("listLimits(%d) = %v, want %v", tc.full, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("listLimits(%d) = %v, want %v", tc.full, got, tc.want)
				break
			}
		}
	}
}

// An exhausted listing is retried SMALLER, not repeated: asking again for the
// same too-expensive query is the thing that does not work.
func TestListPRsHalvesOnExhaustion(t *testing.T) {
	var asked []string
	restore := stubRunGHOnce(func(args []string) ([]byte, error) {
		limit := flagValue(args, "--limit")
		asked = append(asked, limit)
		if limit != "75" {
			return nil, errors.New("gh pr list: HTTP 502: 502 Bad Gateway")
		}
		return []byte(`[{"number":1}]`), nil
	})
	defer restore()

	cfg := config.Config{Repos: []string{"o/a"}}
	limit := 300
	cfg.Discovery.ListLimit = &limit
	d := New(staticConfig(cfg), &fakeStore{}, nil)
	var warns []string
	d.warnf = func(format string, args ...any) { warns = append(warns, fmt.Sprintf(format, args...)) }

	prs, err := d.ghListPRs(context.Background(), "o/a")
	if err != nil {
		t.Fatalf("ghListPRs = %v, want success at the third rung", err)
	}
	if len(prs) != 1 {
		t.Fatalf("got %d PRs, want 1", len(prs))
	}
	if len(asked) != 3 || asked[0] != "300" || asked[1] != "150" || asked[2] != "75" {
		t.Fatalf("limits asked = %v, want [300 150 75]", asked)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "degraded to limit 75") {
		t.Fatalf("expected one degraded-depth warning, got %v", warns)
	}
}

// A degraded cycle must not become the new setting: the next sweep asks for
// full depth again.
func TestListPRsReturnsToFullDepth(t *testing.T) {
	exhausted := true
	var asked []string
	restore := stubRunGHOnce(func(args []string) ([]byte, error) {
		limit := flagValue(args, "--limit")
		asked = append(asked, limit)
		if exhausted && limit == "300" {
			return nil, errors.New("gh pr list: HTTP 502: 502 Bad Gateway")
		}
		return []byte(`[]`), nil
	})
	defer restore()

	cfg := config.Config{Repos: []string{"o/a"}}
	limit := 300
	cfg.Discovery.ListLimit = &limit
	d := New(staticConfig(cfg), &fakeStore{}, nil)
	d.warnf = func(string, ...any) {}

	if _, err := d.ghListPRs(context.Background(), "o/a"); err != nil {
		t.Fatalf("degraded sweep: %v", err)
	}
	exhausted = false
	mark := len(asked)
	if _, err := d.ghListPRs(context.Background(), "o/a"); err != nil {
		t.Fatalf("recovered sweep: %v", err)
	}
	if got := asked[mark:]; len(got) != 1 || got[0] != "300" {
		t.Fatalf("next sweep asked %v, want [300]: depth must not stay degraded", got)
	}
}

// A permanent failure gets one attempt at each size only if it looks
// transient; a 404 is the same answer at every depth.
func TestListPRsDoesNotLadderPermanentFailure(t *testing.T) {
	calls := 0
	restore := stubRunGHOnce(func([]string) ([]byte, error) {
		calls++
		return nil, errors.New("gh pr list: HTTP 404: Not Found")
	})
	defer restore()

	d := New(staticConfig(config.Config{Repos: []string{"o/a"}}), &fakeStore{}, nil)
	if _, err := d.ghListPRs(context.Background(), "o/a"); err == nil {
		t.Fatal("want an error on a 404")
	}
	if calls != 1 {
		t.Fatalf("gh called %d times on a 404, want 1", calls)
	}
}

func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// stubRunGHOnce swaps the single-shot gh call for fn, which sees the argv.
func stubRunGHOnce(fn func(args []string) ([]byte, error)) func() {
	old, oldDelay := runGHOnce, ghRetryDelay
	ghRetryDelay = 0
	runGHOnce = func(_ context.Context, args ...string) ([]byte, error) { return fn(args) }
	return func() { runGHOnce, ghRetryDelay = old, oldDelay }
}

// --- candidates.require_review_request ---

// readyPR is a PR nobody has been assigned to: open, not a draft, no review
// requested. The default gate rejects it; the knob is what a team that opens
// PRs ready and lets people pick them up needs.
func readyPR() ghPR {
	return ghPR{
		Number:     7,
		Author:     ghActor{Login: "alice"},
		HeadRefOID: "sha",
		CreatedAt:  fixedNow().Add(-24 * time.Hour),
		UpdatedAt:  fixedNow().Add(-24 * time.Hour),
	}
}

func TestRequireReviewRequestGate(t *testing.T) {
	pr := readyPR()
	if ok, reason := candidacyGate(pr, true); ok || reason != "no open review request" {
		t.Errorf("required: gate = (%v, %q), want (false, no open review request)", ok, reason)
	}
	if ok, reason := candidacyGate(pr, false); !ok || reason != "" {
		t.Errorf("not required: gate = (%v, %q), want (true, \"\")", ok, reason)
	}
}

// The knob relaxes ONE gate. A draft is still not ready, and an approved PR is
// still nothing for this tool to do, whichever way it is set.
func TestRequireReviewRequestRelaxesOnlyItsOwnGate(t *testing.T) {
	draft := readyPR()
	draft.IsDraft = true
	if ok, reason := candidacyGate(draft, false); ok || reason != "draft" {
		t.Errorf("draft = (%v, %q), want (false, draft) even with the request gate off", ok, reason)
	}
	approved := readyPR()
	approved.ReviewDecision = "APPROVED"
	if ok, reason := candidacyGate(approved, false); ok || reason != "already approved" {
		t.Errorf("approved = (%v, %q), want (false, already approved) even with the request gate off", ok, reason)
	}
}

func TestRequireReviewRequestDefaultsToRequiring(t *testing.T) {
	if !(config.Config{}).RequireReviewRequest() {
		t.Fatal("default must require a review request: turning this off changes what every watched repo discovers")
	}
}

// End to end through classify: the same unassigned PR is invisible by default
// and a NEW candidate once the knob is off.
func TestUnassignedPRDiscoveredOnlyWhenKnobIsOff(t *testing.T) {
	for _, tc := range []struct {
		require bool
		want    int
	}{{true, 0}, {false, 1}} {
		fs := &fakeStore{}
		cfg := config.Config{Repos: []string{"o/a"}}
		cfg.Candidates.RequireReviewRequest = config.Bool(tc.require)
		d := New(staticConfig(cfg), fs, nil)
		d.now = fixedNow
		d.lastHumanActivity = func(context.Context, string, int) (time.Time, error) { return time.Time{}, nil }
		d.listPRs = func(context.Context, string) ([]ghPR, error) { return []ghPR{readyPR()}, nil }

		found, err := d.Discover(context.Background())
		if err != nil {
			t.Fatalf("require=%v: %v", tc.require, err)
		}
		if len(found) != tc.want {
			t.Errorf("require=%v: discovered %d candidate(s), want %d", tc.require, len(found), tc.want)
		}
		if tc.want == 1 && found[0].Type != store.TypeNew {
			t.Errorf("require=%v: type = %q, want %q", tc.require, found[0].Type, store.TypeNew)
		}
	}
}

// The recheck just before the engine spend has to agree with discovery, or a
// PR found with the knob off is claimed and then immediately skipped.
func TestRecheckHonoursTheSameKnob(t *testing.T) {
	payload := `{"number":7,"state":"OPEN","isDraft":false,"reviewRequests":[],"reviewDecision":"","reviews":[],"headRefOid":"sha"}`
	if ok, reason, _ := stillCandidateFromJSON([]byte(payload), "", "", true); ok || reason != "no open review request" {
		t.Errorf("required: recheck = (%v, %q), want (false, no open review request)", ok, reason)
	}
	if ok, _, _ := stillCandidateFromJSON([]byte(payload), "", "", false); !ok {
		t.Error("not required: recheck must pass, or discovery and the recheck disagree and every such PR is claimed then skipped")
	}
}

// A draft with no review request is the combination the knob makes it easiest
// to get wrong: relaxing the request gate must not let an unfinished PR
// through. Pinned at the gate, through classify, and through the pre-review
// recheck, because all three have to agree.
func TestDraftWithNoReviewRequestIsNeverQueued(t *testing.T) {
	draft := readyPR()
	draft.IsDraft = true

	for _, require := range []bool{true, false} {
		if ok, reason := candidacyGate(draft, require); ok || reason != "draft" {
			t.Errorf("require=%v: gate = (%v, %q), want (false, draft)", require, ok, reason)
		}

		fs := &fakeStore{}
		cfg := config.Config{Repos: []string{"o/a"}}
		cfg.Candidates.RequireReviewRequest = config.Bool(require)
		d := New(staticConfig(cfg), fs, nil)
		d.now = fixedNow
		d.lastHumanActivity = func(context.Context, string, int) (time.Time, error) { return time.Time{}, nil }
		d.listPRs = func(context.Context, string) ([]ghPR, error) { return []ghPR{draft}, nil }

		found, err := d.Discover(context.Background())
		if err != nil {
			t.Fatalf("require=%v: %v", require, err)
		}
		if len(found) != 0 {
			t.Errorf("require=%v: discovered %d draft(s), want 0", require, len(found))
		}
		if len(fs.enqueued) != 0 {
			t.Errorf("require=%v: enqueued %d draft(s), want 0", require, len(fs.enqueued))
		}

		payload := `{"number":7,"state":"OPEN","isDraft":true,"reviewRequests":[],"reviewDecision":"","reviews":[],"headRefOid":"sha"}`
		if ok, reason, _ := stillCandidateFromJSON([]byte(payload), "", "", require); ok || reason != "draft" {
			t.Errorf("require=%v: recheck = (%v, %q), want (false, draft)", require, ok, reason)
		}
	}
}

// A draft that DOES have a reviewer assigned is still a draft. The request
// gate and the draft gate are independent, and draft is checked first so the
// reason names what actually stopped it.
func TestDraftWithAReviewRequestIsStillNotQueued(t *testing.T) {
	draft := readyPR()
	draft.IsDraft = true
	draft.ReviewRequests = []ghActor{{Login: "bob"}}
	for _, require := range []bool{true, false} {
		if ok, reason := candidacyGate(draft, require); ok || reason != "draft" {
			t.Errorf("require=%v: gate = (%v, %q), want (false, draft)", require, ok, reason)
		}
	}
}
