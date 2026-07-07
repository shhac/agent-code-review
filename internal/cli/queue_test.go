package cli

import (
	"context"
	"testing"

	"github.com/shhac/agent-code-review/internal/store"
)

// fakeQueueStore stubs ListQueue; everything else panics via the embedded nil
// interface, so an unexpected store dependency shows up loudly.
type fakeQueueStore struct {
	store.Store
	queue []store.Candidate
}

func (f *fakeQueueStore) ListQueue(context.Context, string) ([]store.Candidate, error) {
	return f.queue, nil
}

// TestFindQueued pins the skip command's lookup: present rows come back with
// their metadata (the head SHA feeds the SKIPPED history row); absent rows
// report found=false, which the command turns into an error instead of a
// dangling outcome.
func TestFindQueued(t *testing.T) {
	s := &fakeQueueStore{queue: []store.Candidate{
		{Repo: "o/r", Number: 1, HeadSHA: "sha1"},
		{Repo: "o/r", Number: 2, HeadSHA: "sha2"},
	}}
	c, found, err := findQueued(context.Background(), s, "o/r", 2)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v, want present", found, err)
	}
	if c.HeadSHA != "sha2" {
		t.Errorf("wrong row: %+v", c)
	}
	if _, found, _ := findQueued(context.Background(), s, "o/r", 3); found {
		t.Error("absent PR must report found=false")
	}
}
