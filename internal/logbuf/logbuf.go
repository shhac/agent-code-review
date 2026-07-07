// Package logbuf is a fixed-capacity, thread-safe ring of recent log lines.
// The serve daemon tees its log sink into one so the dashboard can show a
// live tail without touching files or capturing stderr.
package logbuf

import (
	"fmt"
	"sync"
	"time"
)

// Entry is one captured log line.
type Entry struct {
	At   time.Time `json:"at"`
	Line string    `json:"line"`
}

type Ring struct {
	mu      sync.Mutex
	entries []Entry
	start   int // index of the oldest entry
	count   int
}

func New(capacity int) *Ring {
	if capacity < 1 {
		capacity = 1
	}
	return &Ring{entries: make([]Entry, capacity)}
}

// Addf formats and appends one line, evicting the oldest when full.
func (r *Ring) Addf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	r.mu.Lock()
	defer r.mu.Unlock()
	e := Entry{At: time.Now(), Line: line}
	if r.count < len(r.entries) {
		r.entries[(r.start+r.count)%len(r.entries)] = e
		r.count++
		return
	}
	r.entries[r.start] = e
	r.start = (r.start + 1) % len(r.entries)
}

// Tail returns up to n of the newest entries, oldest first.
func (r *Ring) Tail(n int) []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n > r.count {
		n = r.count
	}
	if n <= 0 {
		return []Entry{}
	}
	out := make([]Entry, n)
	first := r.start + r.count - n
	for i := range out {
		out[i] = r.entries[(first+i)%len(r.entries)]
	}
	return out
}
