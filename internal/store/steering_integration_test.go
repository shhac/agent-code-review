//go:build integration

package store

import (
	"context"
	"testing"
	"time"
)

// TestSteeringLifecycle pins the steering row's whole life against real
// DuckDB. The CRUD is the cheap part; the SHA gate in Complete is what pays
// for this test. Both of its failure modes are silent:
//
//   - Too eager, and steering survives a completed review, then re-applies to
//     the next review of a re-queued PR: an author's instruction reaching an
//     LLM prompt and a GitHub post it was never authorised for.
//   - Too keen, and it is dropped on the new-commits-mid-review path the
//     comment on Complete explicitly promises will keep it.
//
// Nothing else would fail in either case.
func TestSteeringLifecycle(t *testing.T) {
	ctx := context.Background()
	queued := func(t *testing.T, s Store, sha string) {
		t.Helper()
		if err := s.Enqueue(ctx, Candidate{
			Repo: "o/r", Number: 1, Type: TypeNew, Author: "octocat", HeadSHA: sha,
			CreatedAt: time.Now(), UpdatedAt: time.Now(), DiscoveredAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	steer := func(t *testing.T, s Store, msg string) {
		t.Helper()
		if err := s.SetSteering(ctx, Steering{
			Repo: "o/r", Number: 1, Message: msg, SetBy: "octocat", SetAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	has := func(t *testing.T, s Store) (Steering, bool) {
		t.Helper()
		st, ok, err := s.Steering(ctx, "o/r", 1)
		if err != nil {
			t.Fatal(err)
		}
		return st, ok
	}

	t.Run("set replaces rather than accumulating", func(t *testing.T) {
		s := newTestStore(t)
		queued(t, s, "sha1")
		steer(t, s, "first")
		steer(t, s, "second")
		st, ok := has(t, s)
		if !ok || st.Message != "second" {
			t.Fatalf("steering = %+v ok=%v, want the second message only", st, ok)
		}
		all, err := s.ListSteering(ctx)
		if err != nil || len(all) != 1 {
			t.Errorf("ListSteering = %d rows (err %v), want exactly 1", len(all), err)
		}
	})

	t.Run("clear removes it and is idempotent", func(t *testing.T) {
		s := newTestStore(t)
		queued(t, s, "sha1")
		steer(t, s, "x")
		for i := range 2 {
			if err := s.ClearSteering(ctx, "o/r", 1); err != nil {
				t.Fatalf("clear %d: %v", i, err)
			}
		}
		if _, ok := has(t, s); ok {
			t.Error("steering must be gone after clear")
		}
	})

	t.Run("dequeue takes the steering with the row", func(t *testing.T) {
		s := newTestStore(t)
		queued(t, s, "sha1")
		steer(t, s, "x")
		if err := s.Dequeue(ctx, "o/r", 1); err != nil {
			t.Fatal(err)
		}
		if _, ok := has(t, s); ok {
			t.Error("removing a PR must not leave its steering behind to apply on a re-add")
		}
	})

	t.Run("complete at the reviewed SHA drops both", func(t *testing.T) {
		s := newTestStore(t)
		queued(t, s, "sha1")
		steer(t, s, "x")
		mustClaim(t, s, "o/r", 1, time.Now(), t.TempDir())
		if err := s.Complete(ctx, Review{
			Repo: "o/r", Number: 1, HeadSHA: "sha1", Verdict: VerdictCommented,
			Engine: "codex", ReviewedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		if _, still := getQueued(t, s, "o/r", 1); still {
			t.Error("queue row must be retired")
		}
		if _, ok := has(t, s); ok {
			t.Error("steering must go with the row it guided")
		}
	})

	t.Run("complete at a stale SHA keeps both", func(t *testing.T) {
		// New commits landed mid-review: the row survives with its claim
		// cleared, and the instruction still applies to the re-review.
		s := newTestStore(t)
		queued(t, s, "sha1")
		steer(t, s, "still relevant")
		mustClaim(t, s, "o/r", 1, time.Now(), t.TempDir())
		queued(t, s, "sha2") // discovery advances the head under the claim
		if err := s.Complete(ctx, Review{
			Repo: "o/r", Number: 1, HeadSHA: "sha1", Verdict: VerdictCommented,
			Engine: "codex", ReviewedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		if _, still := getQueued(t, s, "o/r", 1); !still {
			t.Fatal("the row must survive a stale-SHA complete")
		}
		st, ok := has(t, s)
		if !ok || st.Message != "still relevant" {
			t.Errorf("steering must survive alongside the row, got %+v ok=%v", st, ok)
		}
	})
}
