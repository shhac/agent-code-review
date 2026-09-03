package scheduler

import (
	"context"
	"sync"

	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
)

// fakeEngine stands in for every review.Engine the scheduler tests need. It
// began as four near-identical types — a fixed verdict, one that blocked on a
// channel, one that published the context it was invoked with, one that
// computed its verdict per call — which differed only in which of these
// optional fields they had. A nil channel and a nil fn degrade to the simple
// fixed-verdict behaviour, so the plain `&fakeEngine{}` case stays as cheap as
// it was.
//
// Everything a test reads back is mutex-guarded: the dispatcher runs reviews
// on several goroutines at once.
type fakeEngine struct {
	// verdict and err are returned when fn is nil.
	verdict review.Verdict
	err     error
	// fn computes the result per call, for tests that need to act (enqueue
	// more work, count overlap) while a review is running.
	fn func(context.Context, review.Request) (review.Verdict, error)
	// provenance overrides the reported engine identity.
	provenance *review.Provenance

	// started receives the candidate number as each review begins, and
	// release blocks the FIRST review until closed. Together they let a test
	// hold a review in flight and observe what the dispatcher does meanwhile.
	started chan int
	release chan struct{}
	// seen publishes the context each review was invoked with, so a test can
	// assert WHICH of the two shutdown contexts reached the subprocess seam.
	seen chan context.Context

	mu     sync.Mutex
	once   sync.Once
	prompt string
	calls  int
}

func (e *fakeEngine) Review(ctx context.Context, req review.Request) (review.Verdict, error) {
	e.mu.Lock()
	e.prompt = req.Prompt
	e.calls++
	e.mu.Unlock()

	if e.seen != nil {
		e.seen <- ctx
	}
	if e.started != nil {
		e.started <- req.Candidate.Number
	}
	if e.release != nil {
		e.once.Do(func() { <-e.release })
	}
	if e.fn != nil {
		return e.fn(ctx, req)
	}
	return e.verdict, e.err
}

func (e *fakeEngine) Provenance(context.Context) review.Provenance {
	if e.provenance != nil {
		return *e.provenance
	}
	return review.Provenance{Engine: "fake"}
}

// lastPrompt is the prompt the most recent review was given.
func (e *fakeEngine) lastPrompt() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.prompt
}

// callCount is how many reviews the engine has been asked for.
func (e *fakeEngine) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// commented is the ordinary "a review happened" engine.
func commented() *fakeEngine {
	return &fakeEngine{verdict: review.Verdict{Decision: review.DecisionCommented}}
}

// fakeDispatchStore is the scheduler's one store fake: the reviewOne
// recorders, the live queue the dispatcher pulls from, and the reconciliation
// recorders. unused Store methods still panic loudly via the
// embedded nil interface. The queue is mutable under a lock because the
// dispatcher re-reads it on every pull, which is the whole point of the
// design: a test can add work while reviews are already in flight.
type fakeDispatchStore struct {
	fakeSchedStore

	qmu      sync.Mutex
	queue    []store.Candidate
	queueErr error
	pulls    int
	// onList runs inside ListQueue, so a test can change the world (request a
	// shutdown, say) while a pull is in flight.
	onList func()

	// Reconciliation recorders. They live here rather than in a third fake so
	// one type covers the whole SchedulerStore surface: a test spanning
	// reconcile and dispatch needs no fourth fake, and cannot accidentally get
	// the unlocked ListQueue the separate reconcile fake used to have.
	cleared   []int // queue row numbers whose claims were cleared
	abandoned []store.Review
}

func (f *fakeDispatchStore) ListQueue(context.Context, string) ([]store.Candidate, error) {
	f.qmu.Lock()
	defer f.qmu.Unlock()
	f.pulls++
	if f.onList != nil {
		f.onList()
	}
	if f.queueErr != nil {
		return nil, f.queueErr
	}
	return append([]store.Candidate(nil), f.queue...), nil
}

// pullCount reports how many times the dispatcher has listed the queue, which
// is how the lifecycle tests observe that the real dispatcher started.
func (f *fakeDispatchStore) pullCount() int {
	f.qmu.Lock()
	defer f.qmu.Unlock()
	return f.pulls
}

func (f *fakeDispatchStore) enqueue(c store.Candidate) {
	f.qmu.Lock()
	defer f.qmu.Unlock()
	f.queue = append(f.queue, c)
}

// dequeue models what store.Complete does to the real queue: the row is gone
// once its outcome is recorded. Without it the dispatcher would re-offer a
// reviewed candidate forever, which is a property of the fake, not the code.
func (f *fakeDispatchStore) dequeue(repo string, number int) {
	f.qmu.Lock()
	defer f.qmu.Unlock()
	out := f.queue[:0]
	for _, c := range f.queue {
		if c.Repo != repo || c.Number != number {
			out = append(out, c)
		}
	}
	f.queue = out
}

func (f *fakeDispatchStore) Complete(ctx context.Context, r store.Review) error {
	f.dequeue(r.Repo, r.Number)
	return f.fakeSchedStore.Complete(ctx, r)
}

func (f *fakeDispatchStore) AppendHistory(_ context.Context, r store.Review) error {
	f.qmu.Lock()
	defer f.qmu.Unlock()
	f.abandoned = append(f.abandoned, r)
	return nil
}

func (f *fakeDispatchStore) ClearClaim(_ context.Context, _ string, number int) error {
	f.qmu.Lock()
	defer f.qmu.Unlock()
	f.cleared = append(f.cleared, number)
	return nil
}
