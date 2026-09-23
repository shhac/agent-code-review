package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/store"
)

// completeStore records what prune writes. Any other store call panics
// through the nil embedded interface.
type completeStore struct {
	store.Store
	completed []store.Review
}

func (s *completeStore) Complete(_ context.Context, r store.Review) error {
	s.completed = append(s.completed, r)
	return nil
}

// probeLog is the pruner's two gh seams, answering from fixed tables and
// counting what was asked.
type probeLog struct {
	stale    map[int]string // PR number -> recheck failure reason
	states   map[int]string // PR number -> live state
	failing  map[int]bool
	rechecks []int
	stateOf  []int
}

func (l *probeLog) pruner(s store.Store, now time.Time, dryRun bool) pruner {
	return pruner{
		store: s, now: now, lease: 2 * time.Hour, dryRun: dryRun,
		recheck: func(_ context.Context, c store.Candidate) (bool, string, error) {
			l.rechecks = append(l.rechecks, c.Number)
			if l.failing[c.Number] {
				return false, "", errors.New("gh: HTTP 502")
			}
			reason, stale := l.stale[c.Number]
			return !stale, reason, nil
		},
		state: func(_ context.Context, _ string, number int) (string, error) {
			l.stateOf = append(l.stateOf, number)
			if l.failing[number] {
				return "", errors.New("gh: HTTP 502")
			}
			return l.states[number], nil
		},
	}
}

func TestPruneRow(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-10 * time.Minute)
	abandoned := now.Add(-3 * time.Hour)
	discovered := func(n int) store.Candidate {
		return store.Candidate{Repo: "o/r", Number: n, HeadSHA: "sha1", Source: store.SourceDiscovered}
	}
	manual := func(n int) store.Candidate {
		c := discovered(n)
		c.Source = store.SourceManual
		return c
	}
	claimed := func(c store.Candidate, at time.Time) store.Candidate {
		c.ClaimedAt = &at
		return c
	}

	cases := []struct {
		name       string
		c          store.Candidate
		dryRun     bool
		wantAction string
		wantReason string
		wantWrite  bool
		wantProbe  string // "recheck", "state" or ""
	}{
		{"a merged discovered PR is skipped", discovered(1), false, pruneSkipped, "merged", true, "recheck"},
		{"an approved discovered PR is skipped", discovered(2), false, pruneSkipped, "approved", true, "recheck"},
		{"a dry run reports the skip and writes nothing", discovered(1), true, pruneSkipped, "merged", false, "recheck"},
		{"a discovered PR still wanting review is kept", discovered(3), false, pruneKept, "", false, "recheck"},
		{"a recheck gh cannot answer is reported, not raised", discovered(4), false, pruneFailed, "gh: HTTP 502", false, "recheck"},
		{"a merged manual add is warned about and left queued", manual(1), false, pruneWarned, "merged", false, "state"},
		{"a closed manual add is warned about and left queued", manual(5), false, pruneWarned, "closed", false, "state"},
		{"an open manual add is kept without the candidacy gates", manual(2), false, pruneKept, "", false, "state"},
		{"a manual state gh cannot answer is reported", manual(4), false, pruneFailed, "gh: HTTP 502", false, "state"},
		{"a live claim is left to its review", claimed(discovered(1), fresh), false, pruneInFlight, "", false, ""},
		{"an abandoned claim is rechecked like any row", claimed(discovered(1), abandoned), false, pruneSkipped, "merged", true, "recheck"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probes := &probeLog{
				stale:   map[int]string{1: "merged", 2: "approved", 5: "closed"},
				states:  map[int]string{1: "merged", 2: "open", 5: "closed"},
				failing: map[int]bool{4: true},
			}
			s := &completeStore{}
			row, err := probes.pruner(s, now, tc.dryRun).row(context.Background(), tc.c)
			if err != nil {
				t.Fatal(err)
			}
			if row.Action != tc.wantAction || row.Reason != tc.wantReason {
				t.Errorf("row = %+v, want action %q reason %q", row, tc.wantAction, tc.wantReason)
			}
			if tc.wantWrite != (len(s.completed) == 1) || len(s.completed) > 1 {
				t.Fatalf("recorded %+v, want a write: %v", s.completed, tc.wantWrite)
			}
			if tc.wantWrite {
				got := s.completed[0]
				if got.Verdict != store.VerdictSkipped || got.Engine != store.EnginePrecheck || got.HeadSHA != "sha1" {
					t.Errorf("recorded %+v, want a precheck SKIPPED at the queued head", got)
				}
			}
			gotProbe := ""
			switch {
			case len(probes.rechecks) > 0 && len(probes.stateOf) > 0:
				gotProbe = "both"
			case len(probes.rechecks) > 0:
				gotProbe = "recheck"
			case len(probes.stateOf) > 0:
				gotProbe = "state"
			}
			if gotProbe != tc.wantProbe {
				t.Errorf("probed %q, want %q", gotProbe, tc.wantProbe)
			}
			if tc.wantAction == pruneWarned && !strings.Contains(row.Hint, "queue rm o/r ") {
				t.Errorf("warned row hint = %q, want the queue rm command", row.Hint)
			}
		})
	}
}

// One failing row must not abandon the rest, and the summary has to account
// for every row, as ONE document under -f json.
func TestPruneSweep(t *testing.T) {
	buf := captureStdout(t, "json")
	probes := &probeLog{
		stale:   map[int]string{1: "merged"},
		states:  map[int]string{2: "closed"},
		failing: map[int]bool{3: true},
	}
	s := &completeStore{}
	queue := []store.Candidate{
		{Repo: "o/r", Number: 3, Source: store.SourceDiscovered},
		{Repo: "o/r", Number: 1, Source: store.SourceDiscovered},
		{Repo: "o/r", Number: 2, Source: store.SourceManual},
		{Repo: "o/r", Number: 4, Source: store.SourceDiscovered},
	}
	var warnings []string
	warnf := func(notice, _ string) { warnings = append(warnings, notice) }
	if err := probes.pruner(s, time.Now(), false).sweep(context.Background(), queue, warnf); err != nil {
		t.Fatal(err)
	}

	var doc struct {
		Data    []pruneRow     `json:"data"`
		Summary map[string]any `json:"@summary"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("output is not one JSON document: %v\n%s", err, buf)
	}
	if len(doc.Data) != 4 {
		t.Fatalf("emitted %d rows, want all 4: %+v", len(doc.Data), doc.Data)
	}
	want := map[string]float64{"checked": 4, "skipped": 1, "warned": 1, "kept": 1, "in_flight": 0, "failed": 1}
	for k, v := range want {
		if doc.Summary[k] != v {
			t.Errorf("summary[%s] = %v, want %v (summary %v)", k, doc.Summary[k], v, doc.Summary)
		}
	}
	if len(s.completed) != 1 || s.completed[0].Number != 1 {
		t.Errorf("recorded %+v, want only the merged discovered PR", s.completed)
	}
	if len(warnings) != 2 {
		t.Errorf("warnings = %q, want one for the manual add and one for the failure", warnings)
	}
}
