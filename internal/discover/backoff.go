package discover

import (
	"time"
)

// A repo that GitHub is failing on is skipped outright for a while rather
// than re-attempted every cycle. Separate from the page-size ladder in
// retry.go: the ladder survives one expensive query inside a single call,
// this survives a repo that is simply down, across sweeps.

// repoBackoff is one repo's consecutive-failure state, held across sweeps so
// a repo GitHub is failing on is skipped outright rather than re-attempted
// (and re-logged) every cycle.
type repoBackoff struct {
	failures int
	until    time.Time
}

// Backoff bounds, deliberately not configurable: they are a property of how
// GitHub misbehaves, not of any one deployment. Doubling from 2m caps at 30m,
// so a repo that is genuinely down costs two sweeps an hour rather than twelve,
// and a single transient blip (already absorbed by runGH's retries) expires
// before the next cycle would have run anyway.
const (
	backoffBase = 2 * time.Minute
	backoffMax  = 30 * time.Minute
)

func backoffFor(failures int) time.Duration {
	d := backoffBase
	for i := 1; i < failures; i++ {
		if d >= backoffMax {
			break
		}
		d *= 2
	}
	if d > backoffMax {
		d = backoffMax
	}
	return d
}

// inBackoff reports whether repo is still inside the window a previous
// failure opened, with the failure count and expiry for the log line.
func (d *Discoverer) inBackoff(repo string, now time.Time) (time.Time, int, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, ok := d.backoff[repo]
	if !ok || !now.Before(b.until) {
		return time.Time{}, 0, false
	}
	return b.until, b.failures, true
}

// noteFailure records one failed listing and returns the new consecutive
// failure count and the window it opens.
func (d *Discoverer) noteFailure(repo string, now time.Time) (int, time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.backoff == nil {
		d.backoff = map[string]repoBackoff{}
	}
	b := d.backoff[repo]
	b.failures++
	wait := backoffFor(b.failures)
	b.until = now.Add(wait)
	d.backoff[repo] = b
	return b.failures, wait
}

// noteSuccess clears any backoff and returns how many failures it forgave, so
// the caller can log a recovery exactly once.
func (d *Discoverer) noteSuccess(repo string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, ok := d.backoff[repo]
	if !ok {
		return 0
	}
	delete(d.backoff, repo)
	return b.failures
}
