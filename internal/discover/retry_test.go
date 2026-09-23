package discover

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-code-review/internal/config"
)

// What a sweep COSTS when GitHub is misbehaving, measured in calls we did
// not have to make: the same-request retry, the page-size ladder, the
// cross-cycle backoff those two feed, and the budget that bounds them.

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
		// The default depth: the ladder must reach the floor, not stall at a
		// rung the repo that needs it is still too big to answer.
		{300, []int{300, 150, 75, 37, 25}},
		// A depth deeper than the default reaches the floor too. A ladder
		// whose length was a hand-set count stopped here at 31, which is the
		// same failure as stopping at 75, one config change away.
		{500, []int{500, 250, 125, 62, 31, 25}},
		{100, []int{100, 50, 25}},
		{60, []int{60, 30, 25}},
		// At or below the floor there is nothing to give up: one attempt,
		// not several identical ones. Halving from here would clamp back up
		// to the floor and ask for MORE than the rung before it.
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

// The ladder is only worth having if it is walked to the bottom. Every rung
// must be asked, in descending order, and the error the caller sees must be
// the FLOOR's -- an operator reading "backing off" needs to know the cheapest
// question also failed, not the most expensive one.
func TestListPRsExhaustsEveryRungBeforeGivingUp(t *testing.T) {
	var asked []string
	restore := stubRunGHOnce(func(args []string) ([]byte, error) {
		limit := flagValue(args, "--limit")
		asked = append(asked, limit)
		// Distinct per rung: an implementation that kept the FIRST error
		// would otherwise pass this test unnoticed.
		return nil, fmt.Errorf("gh pr list: HTTP 502: 502 Bad Gateway at limit %s", limit)
	})
	defer restore()

	d := New(staticConfig(config.Config{Repos: []string{"o/a"}}), &fakeStore{}, nil)
	d.warnf = func(string, ...any) {}

	_, err := d.ghListPRs(context.Background(), "o/a")
	if err == nil {
		t.Fatal("want an error when every rung fails")
	}
	want := []string{"300", "150", "75", "37", "25"}
	if len(asked) != len(want) {
		t.Fatalf("limits asked = %v, want %v", asked, want)
	}
	for i := range want {
		if asked[i] != want[i] {
			t.Fatalf("limits asked = %v, want %v", asked, want)
		}
	}
	if !strings.Contains(err.Error(), "at limit 25") {
		t.Errorf("error = %q, want the floor rung's failure: the last question asked is the one worth reporting", err)
	}
}

// An exhausted ladder is ONE repo failure, not five. The ladder is how a repo
// spends a single cycle's attempt; if each rung counted, a repo would be
// thrown into deep backoff by its first bad moment.
func TestLadderExhaustionCountsAsOneRepoFailure(t *testing.T) {
	calls := map[string]int{}
	restore := stubRunGHOnce(func(args []string) ([]byte, error) {
		repo := flagValue(args, "--repo")
		calls[repo]++
		if repo == "o/sick" {
			return nil, errors.New("gh pr list: HTTP 502: 502 Bad Gateway")
		}
		return []byte(`[]`), nil
	})
	defer restore()

	cfg := config.Config{Repos: []string{"o/sick", "o/well"}}
	cfg.Discovery.Interval = "5m"
	// The production wiring, deliberately not stubbed: this is the one test
	// that holds the real ladder and the sweep's failure accounting together.
	d := New(staticConfig(cfg), &fakeStore{}, nil)
	now := fixedNow()
	d.now = func() time.Time { return now }
	d.warnf = func(string, ...any) {}

	if _, err := d.Discover(context.Background()); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if calls["o/sick"] != 5 {
		t.Errorf("sick repo made %d gh calls, want 5 (the whole ladder, within one cycle)", calls["o/sick"])
	}
	if calls["o/well"] != 1 {
		t.Errorf("healthy repo made %d gh calls, want 1: one repo's bad cycle must not cost another repo anything", calls["o/well"])
	}
	_, fails, held := d.inBackoff("o/sick", now)
	if !held || fails != 1 {
		t.Errorf("after an exhausted ladder: held=%v fails=%d, want held=true fails=1", held, fails)
	}
	if _, _, held := d.inBackoff("o/well", now); held {
		t.Error("healthy repo is in backoff")
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

// --- transient-failure retry ---

func TestTransientClassification(t *testing.T) {
	retry := []string{
		"gh pr list: HTTP 502: 502 Bad Gateway (https://api.github.com/graphql)",
		"gh api: HTTP 503: Service Unavailable",
		"gh pr list: HTTP 504: Gateway Timeout",
		"gh api: read tcp: connection reset by peer",
		"gh api: net/http: TLS handshake timeout",
	}
	for _, msg := range retry {
		if !transient(msg) {
			t.Errorf("transient(%q) = false, want true", msg)
		}
	}
	// A 4xx, a bad query, or a missing repo fails identically however many
	// times we ask; retrying only delays the error.
	permanent := []string{
		"gh pr list: HTTP 404: Not Found",
		"gh pr list: HTTP 401: Bad credentials",
		"gh api: GraphQL: Field 'nope' doesn't exist",
		"gh pr list: could not resolve to a Repository",
	}
	for _, msg := range permanent {
		if transient(msg) {
			t.Errorf("transient(%q) = true, want false", msg)
		}
	}
}

func TestRunGHRetriesTransientThenSucceeds(t *testing.T) {
	restore := fakeGH(t, `
if [ ! -f "$STATE" ]; then echo 1 > "$STATE"; echo "HTTP 502: 502 Bad Gateway" >&2; exit 1; fi
echo '[]'
`)
	defer restore()

	out, err := runGH(context.Background(), "pr", "list")
	if err != nil {
		t.Fatalf("runGH after one 502 = %v, want success on the retry", err)
	}
	if strings.TrimSpace(string(out)) != "[]" {
		t.Fatalf("runGH returned %q, want the retry's output", out)
	}
}

func TestRunGHGivesUpAfterAttempts(t *testing.T) {
	restore := fakeGH(t, `echo "HTTP 502: 502 Bad Gateway" >&2; exit 1`)
	defer restore()

	if _, err := runGH(context.Background(), "pr", "list"); err == nil {
		t.Fatal("runGH with a permanently 502ing gh = nil, want the last error")
	}
	if got := attempts(t); got != ghAttempts {
		t.Fatalf("gh invoked %d times, want %d", got, ghAttempts)
	}
}

func TestRunGHDoesNotRetryPermanentFailure(t *testing.T) {
	restore := fakeGH(t, `echo "HTTP 404: Not Found" >&2; exit 1`)
	defer restore()

	if _, err := runGH(context.Background(), "pr", "list"); err == nil {
		t.Fatal("runGH on a 404 = nil, want an error")
	}
	if got := attempts(t); got != 1 {
		t.Fatalf("gh invoked %d times on a 404, want 1 (no retries)", got)
	}
}

// fakeGH puts a stub `gh` on PATH running body, with $STATE pointing at a
// scratch file the body may use to vary its behaviour between invocations,
// and $COUNT at a tally of invocations. It zeroes the retry delay so the
// retry path costs the suite nothing.
func fakeGH(t *testing.T, body string) func() {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nSTATE=" + dir + "/state\nprintf x >> " + dir + "/count\n" + body + "\n"
	if err := os.WriteFile(dir+"/gh", []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	oldDelay := ghRetryDelay
	ghRetryDelay = 0
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ghDir = dir
	return func() { ghRetryDelay = oldDelay }
}

var ghDir string

func attempts(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile(ghDir + "/count")
	if err != nil {
		return 0
	}
	return len(b)
}
