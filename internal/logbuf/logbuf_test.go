package logbuf

import (
	"fmt"
	"sync"
	"testing"
)

func lines(es []Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Line
	}
	return out
}

func TestTailBeforeFull(t *testing.T) {
	r := New(4)
	r.Addf("a")
	r.Addf("b %d", 2)
	got := lines(r.Tail(10))
	if len(got) != 2 || got[0] != "a" || got[1] != "b 2" {
		t.Fatalf("Tail = %v, want [a, b 2]", got)
	}
}

func TestEvictionAndOrder(t *testing.T) {
	r := New(3)
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		r.Addf("%s", s)
	}
	got := lines(r.Tail(10))
	if len(got) != 3 || got[0] != "c" || got[2] != "e" {
		t.Fatalf("Tail = %v, want [c, d, e]", got)
	}
	if last := r.Tail(1); len(last) != 1 || last[0].Line != "e" {
		t.Fatalf("Tail(1) = %+v, want line e", last)
	}
}

func TestTailZeroAndEmpty(t *testing.T) {
	r := New(2)
	if got := r.Tail(5); len(got) != 0 {
		t.Fatalf("Tail on empty ring = %v, want []", got)
	}
	r.Addf("x")
	if got := r.Tail(0); len(got) != 0 {
		t.Fatalf("Tail(0) = %v, want []", got)
	}
}

func TestCapacityClamp(t *testing.T) {
	r := New(0)
	r.Addf("only")
	if got := lines(r.Tail(5)); len(got) != 1 || got[0] != "only" {
		t.Fatalf("Tail = %v, want [only]", got)
	}
}

// The ring's reason to exist is concurrent writers (scheduler, discovery,
// HTTP server) against a polling reader; exercise that under -race.
func TestConcurrentAddAndTail(t *testing.T) {
	r := New(16)
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				r.Addf("writer %d line %d", w, i)
			}
		}(w)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			for _, e := range r.Tail(16) {
				if e.Line == "" {
					panic(fmt.Sprintf("empty line surfaced at read %d", i))
				}
			}
		}
	}()
	wg.Wait()
	<-done
	if got := r.Tail(100); len(got) != 16 {
		t.Fatalf("Tail after concurrent writes = %d entries, want 16", len(got))
	}
}
