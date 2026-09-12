// Package score turns a completed review into points for the PR's author.
//
// It is deliberately pure: config in, facts in, a number out, no I/O and no
// error return. The scheduler, the CLI and the dashboard all score through
// this one function, so a leaderboard total and a history row cannot disagree
// about what a review was worth.
//
// It does not import store. Store rows carry a frozen Result, so store would
// have to import this package; keeping the dependency one-way means the
// verdict arrives as a plain string and the canonical values are restated
// here rather than shared.
package score

import (
	"math"
	"strings"
)

// The verdicts that score. Mirrors store's constants rather than importing
// them (see the package comment). Anything else, including SKIPPED and ERROR,
// scores nothing: they are outcomes of OUR machinery, not feedback to the
// author, and they must not cost somebody points.
const (
	verdictApproved         = "APPROVED"
	verdictCommented        = "COMMENTED"
	verdictRequestedChanges = "REQUESTED_CHANGES"
)

// Rules is the RESOLVED scoring policy: every default filled in, every value
// validated. config owns the on-disk shape (where a dial is a *float64 so an
// explicit 0 is distinguishable from unset) and resolves into this, which has
// no optionality left to reason about.
//
// Four dials decide what a PR is worth, and each answers a question somebody
// can hold an opinion about. There were eight and a hand-drawn ladder of
// tiers, which between them could express policies that contradicted each
// other: the rate curve tuned to make a tighter solve win also made a bigger
// DELETION earn less, and the dial that fixed one broke the other. A
// closed-form curve with named landmarks cannot get into that state.
type Rules struct {
	// PieceLines is the size, in changed lines, that earns the most points
	// PER LINE. It answers "how big should one piece of a split be", and it
	// is where somebody decomposing a large change should aim.
	PieceLines float64 `json:"piece_lines"`
	// SizePoints is the most a single PR can earn for its size alone. It sets
	// the scale of the whole board.
	SizePoints float64 `json:"size_points"`
	// SizeFalloff is how sharply a PR stops being worth more as it grows.
	//
	// It must exceed 2 or there is no peak to fall from, and it is the dial
	// for the loudest judgment this shape makes: how much better a stack of
	// well-sized PRs is than the same change shipped whole. Higher falls
	// harder and widens that gap.
	SizeFalloff float64 `json:"size_falloff"`
	// RemovalPointsPer100 is what a hundred NET removed lines earn, on top of
	// the size reward and independent of it.
	//
	// Per hundred rather than per line because the useful values are small
	// fractions, and a dial somebody has to write as 0.2 is a dial they will
	// mistype. It is divided down in exactly one place.
	RemovalPointsPer100 float64 `json:"removal_points_per_100"`

	Approved         float64 `json:"approved"`
	Commented        float64 `json:"commented"`
	RequestedChanges float64 `json:"requested_changes"`
	AttemptDecay     float64 `json:"attempt_decay"`
	// ExcludePaths and UseGitattributes decide which lines are counted rather
	// than what a counted line is worth, and they are part of the ruleset for
	// exactly one reason: they are HASHED, so changing them marks affected
	// rows stale like any other tuning.
	//
	// They were deliberately left out while a recompute could not honour them:
	// flagging rows that nothing could repair would have been worse than not
	// flagging them. Storing the per-file measurement changed that, so they
	// belong in the hash now. ExcludePaths is sorted on the way in, so
	// reordering a list does not fake a policy change.
	ExcludePaths     []string `json:"exclude_paths,omitempty"`
	UseGitattributes bool     `json:"use_gitattributes"`
}

// DefaultRules is the shipped policy.
//
// What it is tuned to say, in the order people ask:
//
//	+100/-100 (100) beats +1000/-1000 (29)          fewer lines for the same solve
//	+100/-1000 (227) beats +1000/-100 (47)          removing beats adding
//	40 PRs of 50 lines (2000) beat one of 2000 (29) granular beats monolithic
//	100 PRs of 1 line (0) beat nothing at all       granular is not atomised
//
// The last line is the property the previous ruleset could not hold. Points
// per line rose without limit as a PR shrank, so the leaderboard was winnable
// by opening one-line pull requests, and the only defence was a tier that
// paid nothing, which nobody would remember to set. Here the reward is
// QUADRATIC near zero, so N fragments of a change earn about 1/N of shipping
// it whole: the defence is the shape rather than a dial.
func DefaultRules() Rules {
	return Rules{
		PieceLines:          50,
		SizePoints:          100,
		SizeFalloff:         3,
		RemovalPointsPer100: 20,
		Approved:            1.0,
		Commented:           0.25,
		RequestedChanges:    -0.25,
		AttemptDecay:        0.4,
		UseGitattributes:    true,
	}
}

// Input is everything scoring reads about one completed review.
//
// Additions and Deletions are the SCORED counts (generated and vendored files
// already excluded), not the PR's raw totals. Attempt is 1-based: this
// review's index among the real-verdict reviews of that PR.
type Input struct {
	Additions int
	Deletions int
	Verdict   string
	Attempt   int
}

// Result is one review's points, with the size tier that produced them.
//
// Bucket travels with the score because it is the single fact that makes a
// surprising number explain itself. It is now a LABEL rather than a mechanism:
// the tiers used to carry the multipliers, and now they only name where a PR
// sits against the policy's own landmarks.
type Result struct {
	Score  int    `json:"score"`
	Bucket string `json:"bucket"`
}

// Compute scores one review. Total by construction: an unrecognised verdict,
// an attempt below 1, or empty rules score zero, never a panic and never an
// error to handle at a call site that has just spent a review and cannot do
// anything useful with one. Whether a verdict is scorable at all is decided
// upstream, by the one canonical verdict list in store.
func Compute(r Rules, in Input) Result {
	changed := float64(in.Additions + in.Deletions)
	removed := float64(in.Deletions - in.Additions)

	res := Result{Bucket: r.Tier(changed)}

	verdictMult, ok := verdictMultiplier(r, in.Verdict)
	if !ok || in.Attempt < 1 {
		return res
	}

	// Nothing reviewable, nothing earned.
	//
	// Without this guard a PR whose every file is linguist-generated (a
	// lockfile bump, a regenerated client, this repo's own committed
	// internal/dashboard/assets bundle) is scored on an empty diff. Exclusion
	// exists to stop generated lines burying a change, not to mint points for
	// a PR that contains nothing else. It is scored, and the answer is zero.
	if changed <= 0 {
		return res
	}

	// The removal reward is never scaled by a NEGATIVE verdict.
	//
	// Shrinking the codebase must not amplify a penalty: without the clamp a
	// rejected deletion is punished harder than a rejected addition, which
	// rewards exactly the wrong thing. A rejected PR still loses its size
	// reward, which is the part the review was actually about.
	points := verdictMult*r.SizeReward(changed) + math.Max(verdictMult, 0)*r.RemovalReward(removed)
	points *= math.Pow(r.AttemptDecay, float64(in.Attempt-1))

	res.Score = roundHalfAway(points)
	return res
}

// roundHalfAway rounds to a whole point, away from zero at the halfway mark.
//
// Scores are INTEGERS on purpose. A leaderboard shows per-review scores under
// a total, and with fractional scores stored and whole ones displayed the two
// disagree in plain sight: 37.5 + 22.5 + 54 totals 114 while the rows above it
// visibly read 38 + 23 + 54 = 115. Rounding once, at the point the number is
// computed and frozen, makes every sum of displayed rows exact.
func roundHalfAway(f float64) int {
	if math.IsNaN(f) {
		return 0
	}
	// Clamped to the range the history column can actually hold (DuckDB
	// INTEGER is 32-bit). The IsInf guard alone was not enough: a size_points
	// of 1e308 stays FINITE through the arithmetic, so nothing tripped, and
	// the int conversion then saturated to 9223372036854775807 on its way into
	// a 32-bit column. Validate bounds the dials as well; this is the backstop
	// for a config.json edited by hand, which never passes through that check.
	switch {
	case f > maxScore:
		return maxScore
	case f < -maxScore:
		return -maxScore
	}
	return int(math.Round(f))
}

// maxScore is math.MaxInt32: the widest value history.score can store.
const maxScore = 1<<31 - 1

// verdictMultiplier maps a verdict onto its multiplier. ok=false for anything
// that is not a real verdict, which is the SKIPPED/ERROR guard: those rows
// score nothing AND, upstream, do not consume an attempt.
func verdictMultiplier(r Rules, verdict string) (float64, bool) {
	switch strings.ToUpper(strings.TrimSpace(verdict)) {
	case verdictApproved:
		return r.Approved, true
	case verdictCommented:
		return r.Commented, true
	case verdictRequestedChanges:
		return r.RequestedChanges, true
	}
	return 0, false
}
