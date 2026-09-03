package scheduler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/review"
	"github.com/shhac/agent-code-review/internal/store"
)

// fakeSchedStore records the calls reviewOne makes; unused Store methods panic
// so an unexpected dependency shows up loudly. The mutex matters: the
// dispatcher runs reviewOne on several goroutines at once, so the recorders
// must be race-free.
type fakeSchedStore struct {
	store.Store // panic on anything not overridden

	mu    sync.Mutex
	group string // the group every handle resolves to, unless byHandle says otherwise
	// byHandle answers per author. Concurrent reviews can run several engines,
	// so a fake that gives every handle the same group cannot express the case
	// the per-engine floor exists for: one candidate held while another runs.
	byHandle  map[string]string
	groupErr  error // simulate the roster lookup failing
	claimErr  error // simulate the claim itself failing
	claimLost bool  // simulate losing the compare-and-swap to another worker
	claims    []store.Lease
	workDirs  []string
	completed []store.Review
}

func (f *fakeSchedStore) Claim(_ context.Context, _ string, _ int, l store.Lease) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimErr != nil {
		return false, f.claimErr
	}
	if f.claimLost {
		return false, nil
	}
	f.claims = append(f.claims, l)
	f.workDirs = append(f.workDirs, l.WorkDir)
	return true, nil
}

func (f *fakeSchedStore) AuthorGroup(_ context.Context, _, handle string) (config.Membership, error) {
	if f.groupErrFor(handle) != nil {
		return config.Membership{}, f.groupErrFor(handle)
	}
	group := f.group
	if g, ok := f.byHandle[handle]; ok {
		group = g
	}
	return config.Membership{Group: group, Repo: config.WildcardRepo}, nil
}

// groupErrFor scopes the simulated lookup failure: an entry of "" in byHandle
// marks the one author whose row cannot be read, so a test can prove that ONE
// bad lookup holds back the healthy candidates too. A bare groupErr fails for
// every handle.
func (f *fakeSchedStore) groupErrFor(handle string) error {
	if f.groupErr == nil {
		return nil
	}
	if g, ok := f.byHandle[handle]; ok && g != "" {
		return nil
	}
	return f.groupErr
}

func (f *fakeSchedStore) Complete(_ context.Context, r store.Review) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed = append(f.completed, r)
	return nil
}

func newTestScheduler(fs *fakeSchedStore, fe *fakeEngine) *Scheduler {
	return newReviewScheduler(fs, fe, Deps{})
}

// newReviewScheduler is newTestScheduler with room for the test to state the
// dependency it cares about (its own config, or its own candidacy recheck).
func newReviewScheduler(fs *fakeSchedStore, fe *fakeEngine, d Deps) *Scheduler {
	d.Store = fs
	d.NewEngine = fixedEngine(fe)
	if d.Config == nil {
		d.Config = func() config.Config {
			return config.Config{Review: config.ReviewSettings{MainPrompt: "MAIN"}}
		}
	}
	return newScheduler(d)
}

// reviewOne invokes Scheduler.reviewOne with what the dispatcher would hand
// it: the candidate paired with its author's resolved policy, the config
// snapshot it was resolved under, and the injected engine.
func reviewOne(s *Scheduler, fe *fakeEngine, c store.Candidate) error {
	cfg := s.cfg()
	m, err := s.store.AuthorGroup(context.Background(), c.Repo, c.Author)
	if err != nil {
		return err
	}
	return s.reviewOne(context.Background(), pending{
		candidate: c,
		policy:    cfg.ResolvePolicy(c.Repo, c.Author, m),
	}, cfg, fe)
}

// TestReviewOneCompletesEveryOutcome: every decision (real reviews, skips,
// and errors alike) ends as exactly one history row via Complete, carrying
// the reviewed SHA (Complete's delete is gated on it).
func TestReviewOneCompletesEveryOutcome(t *testing.T) {
	decisions := []string{
		review.DecisionApproved,
		review.DecisionCommented,
		review.DecisionRequestedChanges,
		review.DecisionSkipped,
		review.DecisionError,
	}
	for _, decision := range decisions {
		t.Run(decision, func(t *testing.T) {
			fs := &fakeSchedStore{}
			fe := &fakeEngine{verdict: review.Verdict{Decision: decision, Summary: "s", Tokens: review.TokenUsage{Input: 4242}}}
			s := newTestScheduler(fs, fe)

			c := store.Candidate{Repo: "o/r", Number: 5, Author: "alice", HeadSHA: "sha1"}
			if err := reviewOne(s, fe, c); err != nil {
				t.Fatal(err)
			}
			if len(fs.claims) != 1 {
				t.Errorf("candidate must be claimed exactly once, got %d", len(fs.claims))
			}
			if len(fs.workDirs) != 1 || fs.workDirs[0] == "" {
				t.Errorf("claim must record the engine workdir, got %v", fs.workDirs)
			}
			if len(fs.completed) != 1 {
				t.Fatalf("every outcome must Complete exactly once, got %d", len(fs.completed))
			}
			r := fs.completed[0]
			if r.Verdict != decision {
				t.Errorf("verdict = %q, want %q", r.Verdict, decision)
			}
			if r.HeadSHA != "sha1" {
				t.Errorf("history must carry the reviewed SHA, got %q", r.HeadSHA)
			}
			if r.TokensUsed != 4242 {
				t.Errorf("the engine's token count must reach history, got %d", r.TokensUsed)
			}
		})
	}
}

// TestReviewOneEngineErrorStillCompletes: a failed invocation propagates its
// error AND records an ERROR outcome; the queue row must not stay claimed
// forever (the old stuck-at-reviewing bug).
func TestReviewOneEngineErrorStillCompletes(t *testing.T) {
	fs := &fakeSchedStore{}
	fe := &fakeEngine{verdict: review.Verdict{Decision: review.DecisionError}, err: errors.New("boom")}
	s := newTestScheduler(fs, fe)

	err := reviewOne(s, fe, store.Candidate{Repo: "o/r", Number: 5, HeadSHA: "sha1"})
	if err == nil {
		t.Fatal("engine error must propagate")
	}
	if len(fs.completed) != 1 || fs.completed[0].Verdict != review.DecisionError {
		t.Errorf("failed invocation must record an ERROR outcome, got %+v", fs.completed)
	}
}

func TestReviewOneRecordsConfiguredCodexModelAndEffort(t *testing.T) {
	fs := &fakeSchedStore{}
	// Spend rides along with the provenance: both ends of the money path are
	// covered elsewhere (the engine reports CostUSD, the store round-trips it),
	// but this is the glue between them.
	fe := &fakeEngine{
		verdict:    review.Verdict{Decision: review.DecisionCommented, Tokens: review.TokenUsage{Input: 40000, Output: 2575, CacheRead: 150000}, CostUSD: 0.6231},
		provenance: &review.Provenance{Engine: "codex", Model: "gpt-5.6-terra", Effort: "high", EngineVersion: "Codex CLI 0.144.0"},
	}
	s := newTestScheduler(fs, fe)
	if err := s.reviewOne(context.Background(), pending{candidate: store.Candidate{Repo: "o/r", Number: 5, HeadSHA: "sha1"}}, config.Config{}, fe); err != nil {
		t.Fatal(err)
	}
	if len(fs.completed) != 1 {
		t.Fatalf("completed = %d, want 1", len(fs.completed))
	}
	got := fs.completed[0]
	if got.Model != "gpt-5.6-terra" || got.Effort != "high" || got.EngineVersion != "Codex CLI 0.144.0" {
		t.Errorf("provenance = %+v", got)
	}
	if got.TokensUsed != 192575 || got.CostUSD != 0.6231 {
		t.Errorf("spend = %d tokens / $%v, want the engine's reported figures", got.TokensUsed, got.CostUSD)
	}
}

// TestReviewOneClaimRace: losing the compare-and-swap claim to another
// worker (e.g. a second daemon instance sharing the store) must be a clean
// no-op: no engine spend, no outcome recorded, no error.
func TestReviewOneClaimRace(t *testing.T) {
	fs := &fakeSchedStore{claimLost: true}
	fe := &fakeEngine{verdict: review.Verdict{Decision: review.DecisionApproved}}
	s := newTestScheduler(fs, fe)

	if err := reviewOne(s, fe, store.Candidate{Repo: "o/r", Number: 6, HeadSHA: "sha1"}); err != nil {
		t.Fatalf("lost claim must not error, got %v", err)
	}
	if fe.lastPrompt() != "" {
		t.Error("engine must not run when the claim was lost")
	}
	if len(fs.completed) != 0 {
		t.Errorf("no outcome may be recorded for a lost claim, got %+v", fs.completed)
	}
}

// TestReviewOneClaimCarriesIdentity: the lease must record host+pid so boot
// reconciliation can tell this process's claims from a sibling's.
func TestReviewOneClaimCarriesIdentity(t *testing.T) {
	fs := &fakeSchedStore{}
	fe := &fakeEngine{verdict: review.Verdict{Decision: review.DecisionCommented}}
	s := newTestScheduler(fs, fe)
	if err := reviewOne(s, fe, store.Candidate{Repo: "o/r", Number: 6, HeadSHA: "sha1"}); err != nil {
		t.Fatal(err)
	}
	if len(fs.claims) != 1 || fs.claims[0].Host == "" || fs.claims[0].PID <= 0 || fs.claims[0].StaleAfter <= 0 {
		t.Errorf("claim lease must carry host/pid/staleness, got %+v", fs.claims)
	}
}

// TestReviewOneGroupReachesPrompt: the author's roster group must resolve
// through config and flip the approval directive the engine sees. The group
// also carries a prompt fragment, which has to reach the same prompt: that is
// what makes a cohort's instruction a cohort's instruction.
func TestReviewOneGroupReachesPrompt(t *testing.T) {
	cfg := config.Config{
		Authors: config.AuthorSettings{
			Groups: map[string]config.Group{
				"core":       {Review: config.ReviewApprove},
				"contractor": {Review: config.ReviewComment, Prompt: "COHORT-FRAGMENT"},
			},
		},
	}
	run := func(group string) string {
		fs := &fakeSchedStore{group: group}
		fe := &fakeEngine{verdict: review.Verdict{Decision: review.DecisionCommented}}
		s := newReviewScheduler(fs, fe, Deps{Config: func() config.Config { return cfg }})
		if err := reviewOne(s, fe, store.Candidate{Repo: "o/r", Number: 5, Author: "alice"}); err != nil {
			t.Fatal(err)
		}
		return fe.lastPrompt()
	}
	if p := run("core"); !strings.Contains(p, "MAY approve") {
		t.Errorf("an approve-level group must yield MAY-approve directive, got:\n%.200s", p)
	}
	p := run("contractor")
	if !strings.Contains(p, "DO NOT approve") {
		t.Errorf("a comment-level group must yield DO-NOT-approve directive, got:\n%.200s", p)
	}
	if !strings.Contains(p, "COHORT-FRAGMENT") {
		t.Errorf("the group's prompt fragment must reach the engine, got:\n%s", p)
	}
}

// TestReviewOneAuthorLookupError pins the approve-gating junction: when the
// roster lookup fails, the error propagates, the engine is never invoked
// (no prompt is built with a guessed approval policy), and no outcome is
// recorded — the claim stays until the lease window retries it. A refactor
// that "handles" the error by defaulting to a permissive policy must fail
// this test.
func TestReviewOneAuthorLookupError(t *testing.T) {
	fs := &fakeSchedStore{groupErr: errors.New("store unavailable")}
	fe := &fakeEngine{verdict: review.Verdict{Decision: review.DecisionApproved}}
	s := newTestScheduler(fs, fe)

	if err := reviewOne(s, fe, store.Candidate{Repo: "o/r", Number: 5, Author: "alice", HeadSHA: "sha1"}); err == nil {
		t.Fatal("roster lookup error must propagate")
	}
	if fe.lastPrompt() != "" {
		t.Error("engine must not run when the roster lookup failed")
	}
	if len(fs.completed) != 0 {
		t.Errorf("no outcome may be recorded on a lookup error, got %+v", fs.completed)
	}
}

// TestReviewOnePrecheck pins the pre-review revalidation: stale discovered
// candidates are skipped without touching the engine; manual adds bypass the
// check entirely; a recheck error propagates without recording an outcome
// (the stale lease retries it once it ages out).
func TestReviewOnePrecheck(t *testing.T) {
	t.Run("stale discovered candidate records a precheck skip", func(t *testing.T) {
		fs := &fakeSchedStore{}
		fe := &fakeEngine{verdict: review.Verdict{Decision: review.DecisionApproved}}
		s := newReviewScheduler(fs, fe, Deps{StillCandidate: func(context.Context, string, int, string, string) (bool, string, error) {
			return false, "already approved", nil
		}})
		c := store.Candidate{Repo: "o/r", Number: 7, HeadSHA: "sha1", Source: store.SourceDiscovered}
		if err := reviewOne(s, fe, c); err != nil {
			t.Fatal(err)
		}
		if fe.lastPrompt() != "" {
			t.Error("engine must not run for a stale candidate")
		}
		if len(fs.completed) != 1 || fs.completed[0].Verdict != review.DecisionSkipped || fs.completed[0].Engine != store.EnginePrecheck {
			t.Errorf("stale candidate must complete as a precheck SKIPPED, got %+v", fs.completed)
		}
	})

	t.Run("manual candidates bypass the recheck", func(t *testing.T) {
		fs := &fakeSchedStore{}
		fe := &fakeEngine{verdict: review.Verdict{Decision: review.DecisionCommented}}
		s := newReviewScheduler(fs, fe, Deps{StillCandidate: func(context.Context, string, int, string, string) (bool, string, error) {
			t.Error("manual candidate must not be rechecked")
			return false, "", nil
		}})
		c := store.Candidate{Repo: "o/r", Number: 8, HeadSHA: "sha1", Source: store.SourceManual}
		if err := reviewOne(s, fe, c); err != nil {
			t.Fatal(err)
		}
		if len(fs.completed) != 1 || fs.completed[0].Verdict != review.DecisionCommented {
			t.Errorf("manual candidate must be reviewed normally, got %+v", fs.completed)
		}
	})

	t.Run("recheck error propagates and records nothing", func(t *testing.T) {
		fs := &fakeSchedStore{}
		fe := &fakeEngine{}
		s := newReviewScheduler(fs, fe, Deps{StillCandidate: func(context.Context, string, int, string, string) (bool, string, error) {
			return false, "", errors.New("gh unavailable")
		}})
		c := store.Candidate{Repo: "o/r", Number: 9, HeadSHA: "sha1", Source: store.SourceDiscovered}
		if err := reviewOne(s, fe, c); err == nil {
			t.Fatal("recheck error must propagate")
		}
		if len(fs.completed) != 0 {
			t.Errorf("no outcome may be recorded on recheck error, got %+v", fs.completed)
		}
	})
}

// TestReviewOneCleansUpWhenClaimErrors: the workdir is created BEFORE the
// claim so the claim can record it, which means a claim that errors leaves a
// directory nothing points at. Nothing would ever read or remove it, so each
// failure leaked one. The lost-the-claim path already cleaned up; the error
// path returned straight past it.
func TestReviewOneCleansUpWhenClaimErrors(t *testing.T) {
	// Compared against a before-snapshot, because a successful review
	// deliberately leaves its workdir behind for postmortem log access, so
	// every other test in this package leaves some too.
	pattern := filepath.Join(os.TempDir(), "agent-code-review-5-*")
	before, _ := filepath.Glob(pattern)
	existing := make(map[string]bool, len(before))
	for _, dir := range before {
		existing[dir] = true
	}

	fs := &fakeSchedStore{claimErr: errors.New("store unavailable")}
	fe := &fakeEngine{}
	s := newTestScheduler(fs, fe)

	err := reviewOne(s, fe, store.Candidate{Repo: "o/r", Number: 5, HeadSHA: "sha1"})
	if err == nil {
		t.Fatal("a claim error must propagate")
	}
	if len(fs.workDirs) != 0 {
		t.Fatalf("no claim was recorded, so no workdir should be either: %v", fs.workDirs)
	}
	after, _ := filepath.Glob(pattern)
	for _, dir := range after {
		if !existing[dir] {
			t.Errorf("a failed claim leaked its workdir: %s", dir)
		}
	}
}

// TestTail pins the log-tail formatter: whitespace-trimmed, newline-flattened,
// last-n-bytes with an ellipsis when truncated.
func TestTail(t *testing.T) {
	if got := tail("  short\nlines  ", 100); got != "short | lines" {
		t.Errorf("tail = %q", got)
	}
	long := strings.Repeat("x", 600)
	got := tail(long, 500)
	if len([]rune(got)) != 501 || !strings.HasPrefix(got, "…") {
		t.Errorf("tail truncation = %d runes, want 501 with a leading ellipsis", len([]rune(got)))
	}
}

// TestRunOneWorkdirFailureLeavesTheRowUntouched pins the second of the two
// named pre-claim failure paths (the first being an unbuildable engine). Both
// leave the queue row exactly as they found it, which is precisely why the
// dispatcher has to back the candidate off: without that it would be re-offered
// at the head forever. The claim-error path has its own test; this one did not.
func TestRunOneWorkdirFailureLeavesTheRowUntouched(t *testing.T) {
	// A TMPDIR that does not exist makes os.MkdirTemp fail.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))

	fs := &fakeSchedStore{}
	fe := commented()
	s := newTestScheduler(fs, fe)

	err := s.runOne(context.Background(), pending{
		candidate: store.Candidate{Repo: "o/r", Number: 7, HeadSHA: "s7"},
		cfg:       s.cfg(),
	})
	if err == nil {
		t.Fatal("a workdir that cannot be created must fail the attempt")
	}
	if len(fs.claims) != 0 {
		t.Errorf("the candidate must never be claimed, got %+v", fs.claims)
	}
	if len(fs.completed) != 0 {
		t.Errorf("no outcome may be recorded, got %+v", fs.completed)
	}
	if fe.lastPrompt() != "" {
		t.Error("the engine must never run")
	}
}
