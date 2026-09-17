package discover

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Everything this package does to survive a flaky or overloaded gh call.
// Two policies over the same primitives: ask again for the same thing, or
// -- when the thing itself is what GitHub choked on -- ask for less.

// ghAttempts is how many times runGH will try a call whose failure looks
// transient, and ghRetryDelay the base wait between those tries (linear:
// 1x then 2x). Both are vars so tests can drive the retry path without
// sleeping.
var (
	ghAttempts    = 3
	ghRetryDelay  = 1500 * time.Millisecond
	transientHTTP = regexp.MustCompile(`HTTP 5\d\d`)
)

// transient reports whether a gh failure is worth trying again. GitHub's
// GraphQL endpoint answers an expensive query with a 502 often enough that
// treating one as fatal costs a repo its whole discovery cycle, and the same
// query succeeds on the next attempt. Anything else (404, bad auth, a malformed
// query) will fail identically however many times we ask, so it returns at once.
func transient(msg string) bool {
	if transientHTTP.MatchString(msg) {
		return true
	}
	for _, s := range []string{"Bad Gateway", "Service Unavailable", "Gateway Timeout", "connection reset by peer", "unexpected EOF", "TLS handshake timeout"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// runGH executes the gh CLI and returns stdout, surfacing stderr on failure.
// Transient failures are retried; see transient for what counts.
func runGH(ctx context.Context, args ...string) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= ghAttempts; attempt++ {
		if attempt > 1 {
			if err := sleepOrCancel(ctx, time.Duration(attempt-1)*ghRetryDelay); err != nil {
				return nil, err
			}
		}
		out, err := runGHOnce(ctx, args...)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if !transient(err.Error()) {
			return nil, err
		}
	}
	return nil, lastErr
}

// sleepOrCancel waits out a retry delay, or gives up early if the caller has
// already stopped caring. Both of this package's retry shapes need it -- the
// same-request retry here and the page-size ladder in discover.go -- and a
// sweep that is out of budget should not spend its last seconds asleep.
func sleepOrCancel(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// runGHOnce is one execution of the gh CLI, with no retry policy of its own.
// A var so tests can drive the retry and page-size ladders without a stub
// binary on PATH.
var runGHOnce = func(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("gh %s: %s", strings.Join(elideQuery(args), " "), msg)
	}
	return out, nil
}

// elideQuery replaces a GraphQL document in an error's echoed argv with a
// placeholder.
//
// The document is a static multi-line constant, so reproducing it verbatim
// buries the one line that says what went wrong under a dozen that do not. The
// rest of the argv (owner, repo, number) is what a reader needs to reproduce
// the call, and it stays.
func elideQuery(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if strings.HasPrefix(a, "query=") {
			out[i] = "query=<graphql>"
			continue
		}
		out[i] = a
	}
	return out
}

// Page-size ladder for one repo's listing. GitHub answers an expensive
// GraphQL query with a 502 when it runs out of budget building the response,
// and the honest reply to that is to ask for less rather than to ask again
// for the same thing. listMinLimit is the floor: below it the listing is too
// shallow to be worth the call.
const listMinLimit = 25

// listLimits is the descending ladder tried within one sweep of one repo:
// halve the configured depth until it lands on the floor.
//
// How MANY rungs that takes is a consequence of the depth and the floor, not a
// dial of its own. A hand-set attempt count is a promise that the ladder
// reaches the floor, and it is a promise kept only for the depths it happens
// to be large enough for: at 3 the ladder stopped at 75 from the default depth
// of 300, and the repo whose 502s justified the ladder -- hundreds of open
// PRs, an expensive stitched listing -- cannot answer a 75-wide query either.
// Raising the count to 5 bought the default depth its floor and left any
// deployment configuring 500 stranded at 31. Deriving it means the ladder
// reaches the floor at every depth, by construction.
func listLimits(full int) []int {
	// Below the floor there is nothing to give up, and halving would clamp
	// back UP to it: an ascending ladder that asks for more after asking for
	// less is not a retreat.
	if full <= listMinLimit {
		return []int{full}
	}
	limits := []int{full}
	for last := full; last > listMinLimit; {
		next := last / 2
		if next < listMinLimit {
			next = listMinLimit
		}
		limits = append(limits, next)
		last = next
	}
	return limits
}

// ghListPRs fetches open PRs for one repo with the fields we classify on:
// the production listPRs.
//
// Sorted by most recently updated, not by gh's default of most recently
// created, because the limit truncates and the two orderings truncate
// differently. Every candidate type we recognise is a statement about recent
// activity: NEW is bounded by an age window, REFRESHED by a head SHA that
// moved, DISCUSSION by somebody having just spoken. A PR that qualifies has,
// by definition, been updated recently, while its NUMBER says only when it was
// opened. Under created-desc, one repo with 528 open PRs hid 12 PRs with open
// review requests behind 100 newer ones that were mostly drafts.
//
// The ordering is also what makes the page-size ladder cheap. A degraded
// listing drops the LEAST recently active PRs, which are the least likely to
// be candidates, so half a listing is far more than half the value. Depth
// returns to full on the next cycle: the ladder is a way through one bad
// moment, not a new setting.
//
// This is the one gh call that does not use runGH's retry. Retrying an
// exhausted query unchanged is the thing that does not work; the ladder is the
// retry, and it is a better one.
func (d *Discoverer) ghListPRs(ctx context.Context, repo string) ([]ghPR, error) {
	full := d.cfg().DiscoveryListLimit()
	limits := listLimits(full)
	var lastErr error
	for i, limit := range limits {
		if i > 0 {
			if err := sleepOrCancel(ctx, ghRetryDelay); err != nil {
				return nil, err
			}
		}
		out, err := runGHOnce(ctx, "pr", "list",
			"--repo", repo,
			"--state", "open",
			"--limit", strconv.Itoa(limit),
			"--search", "sort:updated-desc",
			"--json", prListFields,
		)
		if err != nil {
			lastErr = err
			if !transient(err.Error()) {
				return nil, err
			}
			continue
		}
		var prs []ghPR
		if err := json.Unmarshal(out, &prs); err != nil {
			return nil, err
		}
		if limit < full {
			d.warnf("discover %s: degraded to limit %d (from %d) after %d exhaustion(s), listed %d PR(s); full depth resumes next cycle",
				repo, limit, full, i, len(prs))
		}
		return prs, nil
	}
	return nil, lastErr
}
