package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
)

// fakeSchedStore records the calls reviewOne makes; unused Store methods panic
// so an unexpected dependency shows up loudly.
type fakeSchedStore struct {
	store.Store // panic on anything not overridden

	allowed  bool
	statuses []string
	recorded []store.Review
}

func (f *fakeSchedStore) SetStatus(_ context.Context, _ string, _ int, status string) error {
	f.statuses = append(f.statuses, status)
	return nil
}

func (f *fakeSchedStore) IsAuthorAllowed(context.Context, string, string) (bool, error) {
	return f.allowed, nil
}

func (f *fakeSchedStore) RecordReview(_ context.Context, r store.Review) error {
	f.recorded = append(f.recorded, r)
	return nil
}

// fakeEngine returns a fixed verdict and captures the prompt it was given.
type fakeEngine struct {
	verdict review.Verdict
	err     error
	prompt  string
}

func (e *fakeEngine) Name() string { return "fake" }
func (e *fakeEngine) Review(_ context.Context, req review.Request) (review.Verdict, error) {
	e.prompt = req.Prompt
	return e.verdict, e.err
}

func newTestScheduler(fs *fakeSchedStore, fe *fakeEngine) *Scheduler {
	cfg := config.Config{Review: config.ReviewSettings{MainPrompt: "MAIN"}}
	return New(cfg, fs, nil, fe, "the-gh-user", nil)
}

// TestReviewOneRecordsOnlyRealReviews pins the Refreshed-detection invariant:
// history rows exist exactly for APPROVED/COMMENTED/REQUESTED_CHANGES.
func TestReviewOneRecordsOnlyRealReviews(t *testing.T) {
	cases := []struct {
		decision   string
		recorded   bool
		lastStatus string
	}{
		{review.DecisionApproved, true, store.StatusReviewed},
		{review.DecisionCommented, true, store.StatusReviewed},
		{review.DecisionRequestedChanges, true, store.StatusReviewed},
		{review.DecisionSkipped, false, store.StatusSkipped},
		{review.DecisionError, false, store.StatusError},
	}
	for _, tc := range cases {
		t.Run(tc.decision, func(t *testing.T) {
			fs := &fakeSchedStore{}
			fe := &fakeEngine{verdict: review.Verdict{Decision: tc.decision, Summary: "s"}}
			s := newTestScheduler(fs, fe)

			c := store.Candidate{Repo: "o/r", Number: 5, Author: "alice", HeadSHA: "sha1"}
			if err := s.reviewOne(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			if got := len(fs.recorded) == 1; got != tc.recorded {
				t.Errorf("recorded=%v, want %v (rows: %+v)", got, tc.recorded, fs.recorded)
			}
			if tc.recorded && fs.recorded[0].HeadSHA != "sha1" {
				t.Errorf("history must carry the reviewed SHA, got %q", fs.recorded[0].HeadSHA)
			}
			if last := fs.statuses[len(fs.statuses)-1]; last != tc.lastStatus {
				t.Errorf("final status = %q, want %q", last, tc.lastStatus)
			}
		})
	}
}

// TestReviewOneEngineErrorNeverRecords: a failed invocation must not create a
// history row even though the driver returns a Verdict alongside the error.
func TestReviewOneEngineErrorNeverRecords(t *testing.T) {
	fs := &fakeSchedStore{}
	fe := &fakeEngine{verdict: review.Verdict{Decision: review.DecisionError}, err: errors.New("boom")}
	s := newTestScheduler(fs, fe)

	err := s.reviewOne(context.Background(), store.Candidate{Repo: "o/r", Number: 5, HeadSHA: "sha1"})
	if err == nil {
		t.Fatal("engine error must propagate")
	}
	if len(fs.recorded) != 0 {
		t.Errorf("failed invocation must not record history: %+v", fs.recorded)
	}
	if last := fs.statuses[len(fs.statuses)-1]; last != store.StatusError {
		t.Errorf("final status = %q, want error", last)
	}
}

// TestReviewOneAllowedFlagReachesPrompt: the store's per-repo answer must flip
// the approval directive the engine sees.
func TestReviewOneAllowedFlagReachesPrompt(t *testing.T) {
	run := func(allowed bool) string {
		fs := &fakeSchedStore{allowed: allowed}
		fe := &fakeEngine{verdict: review.Verdict{Decision: review.DecisionCommented}}
		s := newTestScheduler(fs, fe)
		if err := s.reviewOne(context.Background(), store.Candidate{Repo: "o/r", Number: 5, Author: "alice"}); err != nil {
			t.Fatal(err)
		}
		return fe.prompt
	}
	if p := run(true); !strings.Contains(p, "MAY approve") {
		t.Errorf("allowed author must yield MAY-approve directive, got:\n%.200s", p)
	}
	if p := run(false); !strings.Contains(p, "DO NOT approve") {
		t.Errorf("disallowed author must yield DO-NOT-approve directive, got:\n%.200s", p)
	}
}
