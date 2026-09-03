package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
	"github.com/shhac/agent-code-review/internal/usage"
)

// newDispatchScheduler wires a scheduler whose dispatcher runs without real
// waits: no cooldown between hand-offs, and a millisecond idle poll.
func newDispatchScheduler(fs *fakeDispatchStore, fe review.Engine) *Scheduler {
	cfg := config.Config{
		Review:   config.ReviewSettings{MainPrompt: "MAIN"},
		Schedule: config.ScheduleSettings{Interval: "1ms", DispatchCooldown: "0s"},
	}
	s := New(Deps{Store: fs, Config: func() config.Config { return cfg }, GHUser: "the-gh-user"})
	s.newEngine = func(config.Config, config.Policy) (review.Engine, error) { return fe, nil }
	s.stillCandidate = func(context.Context, string, int, string, string) (bool, string, error) { return true, "", nil }
	return s
}

// drain runs one dispatcher to completion, guarded so a regression that stops
// it terminating fails the test instead of hanging the suite.
func drain(t *testing.T, s *Scheduler) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- s.dispatch(ctx, ctx, true) }()
	select {
	case err := <-errc:
		return err
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("dispatch did not drain: it is re-offering a candidate that never completes")
		return nil
	}
}

// TestDispatchGracefulStop pins serve's first-Ctrl-C behavior: stop
// dispatching new reviewers, but let already-started reviewers finish.
func TestDispatchGracefulStop(t *testing.T) {
	fs := &fakeDispatchStore{queue: []store.Candidate{
		{Repo: "o/r", Number: 1, HeadSHA: "s1"},
		{Repo: "o/r", Number: 2, HeadSHA: "s2"},
	}}
	fe := &fakeEngine{started: make(chan int, 2), release: make(chan struct{}), verdict: review.Verdict{Decision: review.DecisionCommented}}
	s := newDispatchScheduler(fs, fe)
	s.cfg = func() config.Config {
		return config.Config{
			Review:   config.ReviewSettings{MainPrompt: "MAIN"},
			Schedule: config.ScheduleSettings{MaxParallel: 1, Interval: "1ms", DispatchCooldown: "0s"},
		}
	}
	gracefulCtx, stop := context.WithCancel(context.Background())
	defer stop()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.dispatch(gracefulCtx, context.Background(), false)
	}()

	if got := <-fe.started; got != 1 {
		t.Fatalf("first reviewer = #%d, want #1", got)
	}
	stop()
	select {
	case got := <-fe.started:
		t.Fatalf("graceful stop must not dispatch a second reviewer, dispatched #%d", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(fe.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dispatch did not wait for the in-flight reviewer")
	}
	if len(fs.completed) != 1 || fs.completed[0].Number != 1 {
		t.Errorf("only the in-flight reviewer should complete, got %+v", fs.completed)
	}
}

// TestDispatchStartsNothingAfterStopRequested pins the check that cannot be
// left to a select: a select with a ready case chooses uniformly at random,
// so a pull that is ready to hand off would start a review roughly half the
// time after shutdown was requested, costing a whole engine invocation.
func TestDispatchStartsNothingAfterStopRequested(t *testing.T) {
	fs := &fakeDispatchStore{queue: []store.Candidate{
		{Repo: "o/r", Number: 1, HeadSHA: "s1"},
		{Repo: "o/r", Number: 2, HeadSHA: "s2"},
	}}
	fe := commented()
	s := newDispatchScheduler(fs, fe)
	gracefulCtx, stop := context.WithCancel(context.Background())
	stop() // already cancelled before the dispatcher ever looks

	if err := s.dispatch(gracefulCtx, context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if len(fs.claims) != 0 || len(fs.completed) != 0 {
		t.Errorf("a cancelled dispatcher must claim and complete nothing, got claims=%d completed=%d",
			len(fs.claims), len(fs.completed))
	}
}

// TestDispatchStopDuringAPullDispatchesNothing closes the window between the
// pre-pull cancellation check and the hand-off. A pull is not instant: it
// lists the queue and resolves an author policy, each a DuckDB subprocess
// behind a global mutex. A stop landing in that window used to be missed
// entirely, and the candidate that was dispatchable when the loop looked got
// handed to a worker anyway, costing exactly the engine invocation the first
// Ctrl-C promises not to start.
func TestDispatchStopDuringAPullDispatchesNothing(t *testing.T) {
	fs := &fakeDispatchStore{queue: []store.Candidate{{Repo: "o/r", Number: 1, HeadSHA: "s1"}}}
	gracefulCtx, stop := context.WithCancel(context.Background())
	defer stop()
	fs.onList = func() { stop() } // the stop lands mid-pull

	fe := commented()
	s := newDispatchScheduler(fs, fe)

	done := make(chan error, 1)
	go func() { done <- s.dispatch(gracefulCtx, context.Background(), false) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch did not return after the stop")
	}
	if len(fs.claims) != 0 || len(fs.completed) != 0 {
		t.Errorf("a candidate pulled before the stop must be discarded, not dispatched; got claims=%d completed=%d",
			len(fs.claims), len(fs.completed))
	}
}

// TestDispatch pins the consumer's orchestration: review everything the queue
// makes available, record each outcome, and leave held or claimed rows alone.
func TestDispatch(t *testing.T) {
	t.Run("reviews every available candidate", func(t *testing.T) {
		fresh := time.Now().Add(-time.Minute)
		fs := &fakeDispatchStore{queue: []store.Candidate{
			{Repo: "o/r", Number: 1, HeadSHA: "s1"},
			{Repo: "o/r", Number: 2, HeadSHA: "s2"},
			{Repo: "o/r", Number: 3, HeadSHA: "s3", ClaimedAt: &fresh}, // in flight elsewhere
		}}
		fe := commented()
		s := newDispatchScheduler(fs, fe)

		if err := drain(t, s); err != nil {
			t.Fatal(err)
		}
		if len(fs.completed) != 2 {
			t.Errorf("both unclaimed candidates must complete, got %d", len(fs.completed))
		}
		for _, r := range fs.completed {
			if r.Number == 3 {
				t.Error("a freshly claimed candidate must not be re-reviewed")
			}
		}
	})

	// The reason this design exists: under the batch cycle a candidate that
	// arrived after the snapshot waited for every in-flight review to finish,
	// even with slots free.
	t.Run("picks up work queued after dispatching began", func(t *testing.T) {
		fs := &fakeDispatchStore{queue: []store.Candidate{{Repo: "o/r", Number: 1, HeadSHA: "s1"}}}
		queued := make(chan struct{})
		var once sync.Once
		fe := &fakeEngine{fn: func(_ context.Context, req review.Request) (review.Verdict, error) {
			// Add the second PR while the first review is still running, the
			// way discovery or a manual add would.
			once.Do(func() {
				fs.enqueue(store.Candidate{Repo: "o/r", Number: 2, HeadSHA: "s2"})
				close(queued)
			})
			return review.Verdict{Decision: review.DecisionCommented}, nil
		}}
		s := newDispatchScheduler(fs, fe)
		if err := drain(t, s); err != nil {
			t.Fatal(err)
		}
		<-queued
		if len(fs.completed) != 2 {
			t.Fatalf("the late arrival must be reviewed in the same drain, got %d outcomes", len(fs.completed))
		}
	})

	t.Run("queue error aborts a drain", func(t *testing.T) {
		fs := &fakeDispatchStore{queueErr: errors.New("db gone")}
		s := newDispatchScheduler(fs, &fakeEngine{})
		if err := drain(t, s); err == nil {
			t.Fatal("a drain must surface the queue error so `run` exits non-zero")
		}
		if len(fs.completed) != 0 {
			t.Errorf("nothing may be recorded, got %+v", fs.completed)
		}
	})

	// An unbuildable engine is per candidate: a group pointing at a broken
	// engine must not stop everyone else. Its own candidate is left pending
	// and retryable (never claimed, never completed).
	//
	// Under the batch cycle that was free, because the cycle walked a
	// snapshot. The dispatcher always offers the queue's head, so without a
	// backoff this candidate would be re-offered forever and starve every
	// candidate behind it: this test is the guard on that.
	t.Run("engine build error backs off without blocking the queue", func(t *testing.T) {
		fs := &fakeDispatchStore{queue: []store.Candidate{
			{Repo: "o/r", Number: 1, HeadSHA: "s1", Author: "broken"},
			{Repo: "o/r", Number: 2, HeadSHA: "s2", Author: "fine"},
		}}
		fe := commented()
		s := newDispatchScheduler(fs, fe)
		s.newEngine = func(_ config.Config, p config.Policy) (review.Engine, error) {
			if p.Engine == "broken-engine" {
				return nil, errors.New("bad engine")
			}
			return fe, nil
		}
		s.cfg = func() config.Config {
			return config.Config{
				Review:   config.ReviewSettings{Engine: "codex", MainPrompt: "MAIN"},
				Schedule: config.ScheduleSettings{Interval: "1ms", DispatchCooldown: "0s"},
				Authors: config.AuthorSettings{Groups: map[string]config.Group{
					"broken-cohort": {Review: config.ReviewComment, Engine: "broken-engine"},
					"ok-cohort":     {Review: config.ReviewComment, Engine: "codex"},
				}},
			}
		}
		fs.byHandle = map[string]string{"broken": "broken-cohort", "fine": "ok-cohort"}

		if err := drain(t, s); err != nil {
			t.Fatalf("one bad engine must not fail the drain, got %v", err)
		}
		if len(fs.completed) != 1 || fs.completed[0].Number != 2 {
			t.Errorf("the healthy candidate must still be reviewed, got %+v", fs.completed)
		}
		if len(fs.claims) != 1 {
			t.Errorf("only the healthy candidate may be claimed, got %d claims", len(fs.claims))
		}
	})

	t.Run("empty queue records nothing", func(t *testing.T) {
		fs := &fakeDispatchStore{}
		s := newDispatchScheduler(fs, &fakeEngine{})
		if err := drain(t, s); err != nil {
			t.Fatal(err)
		}
		if len(fs.claims) != 0 || len(fs.completed) != 0 {
			t.Errorf("an idle dispatcher must record nothing, got claims=%d completed=%d",
				len(fs.claims), len(fs.completed))
		}
	})

	t.Run("held candidates are skipped; an all-held queue is idle", func(t *testing.T) {
		soon := time.Now().Add(30 * time.Minute)
		fs := &fakeDispatchStore{queue: []store.Candidate{
			{Repo: "o/r", Number: 1, HeadSHA: "s1", EligibleAt: &soon, HoldReason: store.HoldCooldown},
			{Repo: "o/r", Number: 2, HeadSHA: "s2"},
		}}
		fe := commented()
		s := newDispatchScheduler(fs, fe)
		if err := drain(t, s); err != nil {
			t.Fatal(err)
		}
		if len(fs.completed) != 1 || fs.completed[0].Number != 2 {
			t.Errorf("only the eligible candidate may be reviewed, got %+v", fs.completed)
		}

		// Every row held: nothing dispatched, and the drain still ends.
		fs = &fakeDispatchStore{queue: []store.Candidate{
			{Repo: "o/r", Number: 1, HeadSHA: "s1", EligibleAt: &soon, HoldReason: store.HoldSettling},
		}}
		s = newDispatchScheduler(fs, fe)
		if err := drain(t, s); err != nil {
			t.Fatal(err)
		}
		if len(fs.completed) != 0 {
			t.Errorf("all-held queue must dispatch nothing, got %d", len(fs.completed))
		}

		// An expired hold is eligible again.
		past := time.Now().Add(-time.Minute)
		fs = &fakeDispatchStore{queue: []store.Candidate{
			{Repo: "o/r", Number: 3, HeadSHA: "s3", EligibleAt: &past, HoldReason: store.HoldCooldown},
		}}
		s = newDispatchScheduler(fs, fe)
		if err := drain(t, s); err != nil {
			t.Fatal(err)
		}
		if len(fs.completed) != 1 || fs.completed[0].Number != 3 {
			t.Errorf("expired hold must be reviewable, got %+v", fs.completed)
		}
	})

	// A candidate handed to a worker must not be handed to a second one while
	// that review is still running. The claim CAS is the cross-process guard,
	// but in-process it would cost a wasted slot and a temp dir every time.
	t.Run("an in-flight candidate is not dispatched twice", func(t *testing.T) {
		fs := &fakeDispatchStore{queue: []store.Candidate{{Repo: "o/r", Number: 1, HeadSHA: "s1"}}}
		var starts int32
		hold := make(chan struct{})
		fe := &fakeEngine{fn: func(context.Context, review.Request) (review.Verdict, error) {
			starts++
			<-hold
			return review.Verdict{Decision: review.DecisionCommented}, nil
		}}
		s := newDispatchScheduler(fs, fe)
		done := make(chan error, 1)
		go func() { done <- s.dispatch(context.Background(), context.Background(), true) }()

		// Give the dispatcher time for several pulls while the review holds.
		time.Sleep(50 * time.Millisecond)
		close(hold)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("drain never finished")
		}
		if starts != 1 {
			t.Errorf("candidate dispatched %d times, want 1", starts)
		}
	})
}

// TestBackoffFor pins the escalation: each consecutive failure doubles the
// hold, capped, so a permanently broken candidate stops being retried every
// few seconds without ever being retried never.
func TestBackoffFor(t *testing.T) {
	cases := []struct {
		fails int
		want  time.Duration
	}{
		{1, dispatchBackoffBase},
		{2, 2 * dispatchBackoffBase},
		{3, 4 * dispatchBackoffBase},
		{99, dispatchBackoffCap},
	}
	for _, c := range cases {
		if got := backoffFor(c.fails); got != c.want {
			t.Errorf("backoffFor(%d) = %s, want %s", c.fails, got, c.want)
		}
	}
}

// concurrencyEngine records how many reviews are in flight at once, so a test
// can assert the parallelism cap actually caps. It blocks each review until
// released, which is the only way to observe overlap.
type concurrencyEngine struct {
	mu      sync.Mutex
	inFlgt  int
	peak    int
	started chan struct{}
	release chan struct{}
}

func (e *concurrencyEngine) Provenance(context.Context) review.Provenance {
	return review.Provenance{Engine: "concurrency"}
}

func (e *concurrencyEngine) Review(context.Context, review.Request) (review.Verdict, error) {
	e.mu.Lock()
	e.inFlgt++
	if e.inFlgt > e.peak {
		e.peak = e.inFlgt
	}
	e.mu.Unlock()
	e.started <- struct{}{}
	<-e.release
	e.mu.Lock()
	e.inFlgt--
	e.mu.Unlock()
	return review.Verdict{Decision: review.DecisionCommented}, nil
}

func (e *concurrencyEngine) peakSeen() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.peak
}

// TestDispatchRespectsMaxParallel pins the throttle on concurrent engine
// spend. Every other dispatch test runs with one candidate or one slot, so the
// at-capacity path is only ever left by cancellation: an off-by-one here
// (`active > cap` rather than `>=`) would let every queued PR spawn an engine
// at once and the rest of the suite would still pass.
func TestDispatchRespectsMaxParallel(t *testing.T) {
	fs := &fakeDispatchStore{queue: []store.Candidate{
		{Repo: "o/r", Number: 1, HeadSHA: "s1"},
		{Repo: "o/r", Number: 2, HeadSHA: "s2"},
		{Repo: "o/r", Number: 3, HeadSHA: "s3"},
		{Repo: "o/r", Number: 4, HeadSHA: "s4"},
	}}
	fe := &concurrencyEngine{started: make(chan struct{}, 4), release: make(chan struct{})}
	s := newDispatchScheduler(fs, fe)
	s.cfg = func() config.Config {
		return config.Config{
			Review:   config.ReviewSettings{MainPrompt: "MAIN"},
			Schedule: config.ScheduleSettings{MaxParallel: 2, Interval: "1ms", DispatchCooldown: "0s"},
		}
	}

	done := make(chan error, 1)
	go func() { done <- s.dispatch(context.Background(), context.Background(), true) }()

	// Exactly two reviews may be in flight; a third must wait for a slot.
	<-fe.started
	<-fe.started
	select {
	case <-fe.started:
		t.Fatal("a third review started while the cap was 2")
	case <-time.After(100 * time.Millisecond):
	}

	close(fe.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("drain never finished")
	}
	if got := fe.peakSeen(); got != 2 {
		t.Errorf("peak concurrent reviews = %d, want 2", got)
	}
	if len(fs.completed) != 4 {
		t.Errorf("every candidate must still be reviewed, got %d", len(fs.completed))
	}
}

// TestDispatchPicksUpAMaxParallelRaise pins the live re-read: raising the cap
// while reviews are in flight must take effect without a restart, which is the
// property the at-capacity reap arm's `cfg = s.cfg()` exists for.
func TestDispatchPicksUpAMaxParallelRaise(t *testing.T) {
	fs := &fakeDispatchStore{queue: []store.Candidate{
		{Repo: "o/r", Number: 1, HeadSHA: "s1"},
		{Repo: "o/r", Number: 2, HeadSHA: "s2"},
	}}
	fe := &concurrencyEngine{started: make(chan struct{}, 2), release: make(chan struct{})}
	s := newDispatchScheduler(fs, fe)

	var cfgMu sync.Mutex
	maxParallel := 1
	s.cfg = func() config.Config {
		cfgMu.Lock()
		defer cfgMu.Unlock()
		return config.Config{
			Review:   config.ReviewSettings{MainPrompt: "MAIN"},
			Schedule: config.ScheduleSettings{MaxParallel: maxParallel, Interval: "1ms", DispatchCooldown: "0s"},
		}
	}

	done := make(chan error, 1)
	go func() { done <- s.dispatch(context.Background(), context.Background(), true) }()

	<-fe.started // one in flight, cap reached
	select {
	case <-fe.started:
		t.Fatal("a second review started while the cap was 1")
	case <-time.After(50 * time.Millisecond):
	}

	cfgMu.Lock()
	maxParallel = 2
	cfgMu.Unlock()

	select {
	case <-fe.started:
	case <-time.After(5 * time.Second):
		t.Fatal("raising max_parallel mid-flight did not start a second reviewer")
	}

	close(fe.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drain never finished")
	}
}

// TestDispatchStateBackoffExpiry pins the half of the backoff nothing could
// reach before the clock seam: that a held candidate is offered AGAIN once its
// hold lapses, and that a clean finish forgets the candidate rather than
// leaving it holding. A change making the hold permanent, or dropping the
// clear, would silently starve the queue with a green suite.
func TestDispatchStateBackoffExpiry(t *testing.T) {
	clock := time.Now()
	st := newDispatchState(func() time.Time { return clock })

	st.fail("o/r#1")
	if !st.skip("o/r#1") {
		t.Fatal("a just-failed candidate must be held")
	}
	clock = clock.Add(backoffFor(1) - time.Second)
	if !st.skip("o/r#1") {
		t.Error("still inside the hold window, must stay held")
	}
	clock = clock.Add(2 * time.Second)
	if st.skip("o/r#1") {
		t.Error("past the hold window, the candidate must be offered again")
	}

	// A second failure escalates rather than restarting at the base.
	st.fail("o/r#1")
	if got := st.byKey["o/r#1"].fails; got != 2 {
		t.Errorf("consecutive failures = %d, want 2", got)
	}

	// A clean finish forgets it entirely: no hold, and the next failure
	// starts again at the base rather than continuing to escalate.
	st.start("o/r#1")
	st.finish("o/r#1", false)
	if st.skip("o/r#1") {
		t.Error("a cleanly finished candidate must not be held")
	}
	st.fail("o/r#1")
	if got := st.byKey["o/r#1"].fails; got != 1 {
		t.Errorf("failures after a clean finish = %d, want the count reset to 1", got)
	}
}

// TestDispatchStatePrune pins the leak fix: a candidate that leaves the queue
// by a route the dispatcher never sees (merged, dequeued, completed by a
// sibling daemon) must not keep its backoff entry for the life of the process.
// An in-flight candidate is never pruned, because it is not in the listing it
// was dispatched from.
func TestDispatchStatePrune(t *testing.T) {
	st := newDispatchState(time.Now)
	st.fail("o/r#1")  // failed, still queued
	st.fail("o/r#2")  // failed, about to vanish from the queue
	st.start("o/r#3") // in flight, absent from the listing

	st.prune(map[string]bool{"o/r#1": true})

	if _, ok := st.byKey["o/r#1"]; !ok {
		t.Error("a still-queued candidate must keep its backoff")
	}
	if _, ok := st.byKey["o/r#2"]; ok {
		t.Error("a candidate that left the queue must be forgotten")
	}
	if _, ok := st.byKey["o/r#3"]; !ok {
		t.Error("an in-flight candidate must never be pruned")
	}
	if st.active() != 1 {
		t.Errorf("active = %d, want 1", st.active())
	}
}

// TestAvailableCandidates pins the lease semantics: unclaimed rows and stale
// claims are workable; a fresh claim is another worker's in-flight review.
func TestAvailableCandidates(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	staleAfter := 2 * time.Hour
	fresh := now.Add(-time.Hour)
	boundary := now.Add(-staleAfter) // exactly one window old, still leased
	stale := now.Add(-3 * time.Hour)
	queue := []store.Candidate{
		{Number: 1},                       // unclaimed
		{Number: 2, ClaimedAt: &fresh},    // in flight: leave alone
		{Number: 3, ClaimedAt: &stale},    // abandoned: reclaim
		{Number: 4, ClaimedAt: &boundary}, // boundary: still in flight
	}
	got := availableCandidates(queue, now, staleAfter)
	if len(got) != 2 || got[0].Number != 1 || got[1].Number != 3 {
		t.Fatalf("availableCandidates = %+v, want candidates 1 and 3", got)
	}
}

// floored is a snapshot with almost no headroom left; roomy has plenty.
func floored() usage.Snapshot {
	return usage.Snapshot{FetchedAt: time.Now(), Primary: &usage.Window{UsedPercent: 95, WindowMins: 300}}
}

func roomy() usage.Snapshot {
	return usage.Snapshot{FetchedAt: time.Now(), Primary: &usage.Window{UsedPercent: 5, WindowMins: 300}}
}

// TestDispatchUsageFloorIsPerEngine pins the headline of a mixed-engine
// cycle: one engine being out of headroom holds ITS candidates and nothing
// else. A held candidate is never claimed and never completed, so it is a
// hold like a cooldown, not a skip; it runs when the window refills.
//
// The mixed shape is the whole point. Both candidates on one engine could not
// catch a regression that claims or completes a floored candidate, because
// there would be no floored candidate in play to get it wrong about.
func TestDispatchUsageFloorIsPerEngine(t *testing.T) {
	// alice's group reviews on claude, which has room; bob falls through to the
	// default codex engine, which does not.
	cfg := config.Config{
		Review: config.ReviewSettings{Engine: "codex", MainPrompt: "MAIN"},
		Authors: config.AuthorSettings{
			Groups: map[string]config.Group{
				"claude-cohort": {Review: config.ReviewComment, Engine: "claude"},
				"codex-cohort":  {Review: config.ReviewComment, Engine: "codex"},
			},
		},
	}
	fs := &fakeDispatchStore{queue: []store.Candidate{
		{Repo: "o/r", Number: 1, Author: "alice", HeadSHA: "s1"},
		{Repo: "o/r", Number: 2, Author: "bob", HeadSHA: "s2"},
	}}
	fs.byHandle = map[string]string{"alice": "claude-cohort", "bob": "codex-cohort"}
	fe := commented()

	byEngine := func(engine string) usage.Snapshot {
		if engine == "claude" {
			return roomy()
		}
		return floored()
	}
	cfg.Schedule = config.ScheduleSettings{Interval: "1ms", DispatchCooldown: "0s"}
	s := New(Deps{Store: fs, Config: func() config.Config { return cfg }, GHUser: "u", Usage: byEngine})
	s.newEngine = func(config.Config, config.Policy) (review.Engine, error) { return fe, nil }
	s.stillCandidate = func(context.Context, string, int, string, string) (bool, string, error) { return true, "", nil }

	if err := drain(t, s); err != nil {
		t.Fatal(err)
	}
	// The candidate on the engine with headroom runs...
	if len(fs.completed) != 1 || fs.completed[0].Number != 1 {
		t.Fatalf("only alice's PR (claude, has headroom) should complete, got %+v", fs.completed)
	}
	// ...and the one on the floored engine is HELD, not skipped: no claim and
	// no history row, so nothing retires it from the queue.
	if len(fs.claims) != 1 {
		t.Errorf("the floored candidate must never be claimed, got %d claims", len(fs.claims))
	}
	for _, r := range fs.completed {
		if r.Number == 2 {
			t.Errorf("a floored candidate must not be completed (that would retire it): %+v", r)
		}
	}
}

// TestDispatchRosterErrorHoldsOnlyItsOwnCandidate pins the fail-closed
// choice in pullNext: a candidate whose roster row cannot be read is SKIPPED,
// never defaulted, because guessing a policy is how a PR gets approved that
// shouldn't be. The blast radius used to be the whole cycle (resolvePending
// returned on the first error, holding healthy candidates too); the
// dispatcher resolves lazily, one candidate at a time, so the healthy ones
// now run. The unreadable one is left pending and backed off rather than
// re-offered at the head forever.
func TestDispatchRosterErrorHoldsOnlyItsOwnCandidate(t *testing.T) {
	fs := &fakeDispatchStore{queue: []store.Candidate{
		{Repo: "o/r", Number: 2, Author: "unreadable", HeadSHA: "s2"},
		{Repo: "o/r", Number: 1, Author: "healthy", HeadSHA: "s1"},
	}}
	// "healthy" resolves fine; "unreadable" has no entry, so groupErr applies.
	fs.byHandle = map[string]string{"healthy": config.GroupCommenter}
	fs.groupErr = errors.New("store unavailable")
	fe := commented()
	s := newDispatchScheduler(fs, fe)

	if err := drain(t, s); err != nil {
		t.Fatal(err)
	}
	if len(fs.completed) != 1 || fs.completed[0].Number != 1 {
		t.Fatalf("the readable candidate must still be reviewed, got %+v", fs.completed)
	}
	for _, c := range fs.claims {
		if c.WorkDir == "" {
			t.Error("every claim must record a work dir")
		}
	}
	if len(fs.claims) != 1 {
		t.Errorf("the unreadable candidate must never be claimed, got %d claims", len(fs.claims))
	}
}

// TestDispatchAllEnginesFloored: when every candidate is waiting on a floored
// engine, nothing is claimed and nothing is recorded. A floored candidate is
// HELD, not skipped, so it must survive in the queue to run when its window
// refills. The drain must still terminate rather than spinning on a queue it
// will never consume.
func TestDispatchAllEnginesFloored(t *testing.T) {
	fs := &fakeDispatchStore{queue: []store.Candidate{{Repo: "o/r", Number: 1, Author: "alice", HeadSHA: "s1"}}}
	fe := &fakeEngine{}
	cfg := config.Config{Schedule: config.ScheduleSettings{Interval: "1ms", DispatchCooldown: "0s"}}
	s := New(Deps{
		Store:  fs,
		Config: func() config.Config { return cfg },
		GHUser: "u",
		Usage:  func(string) usage.Snapshot { return floored() },
	})
	s.newEngine = func(config.Config, config.Policy) (review.Engine, error) { return fe, nil }

	if err := drain(t, s); err != nil {
		t.Fatalf("a fully floored queue must drain cleanly, got %v", err)
	}
	if len(fs.claims) != 0 || len(fs.completed) != 0 {
		t.Errorf("a floored candidate is held, not skipped: no claim, no outcome; got claims=%+v completed=%+v", fs.claims, fs.completed)
	}
	if len(fs.queue) != 1 {
		t.Errorf("the held candidate must stay queued, got %+v", fs.queue)
	}
}
