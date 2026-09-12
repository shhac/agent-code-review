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

// Bucket is one size tier. MaxChurn is an INCLUSIVE upper bound; the final
// bucket carries 0 and is open-ended.
type Bucket struct {
	Name       string  `json:"name"`
	MaxChurn   float64 `json:"max_churn,omitempty"`
	Multiplier float64 `json:"multiplier"`
}

// Rules is the RESOLVED scoring policy: every default filled in, every value
// validated. config owns the on-disk shape (where a multiplier is a *float64
// so an explicit 0 is distinguishable from unset) and resolves into this,
// which has no optionality left to reason about.
type Rules struct {
	Base float64 `json:"base"`
	// ChurnUnit is how many lines Base pays for. Score scales with churn in
	// units of this, so Base reads as "points for one full unit of
	// well-sized, first-pass-approved work".
	ChurnUnit float64 `json:"churn_unit"`
	// ChurnExponent is how much of a score follows sheer volume.
	//
	// At 1 the score is proportional: double the churn, double the points, and
	// a bigger PR always earns more in total. Below 1 a bigger PR keeps less
	// of it, and past the ladder's peak the total FALLS: the same solve in
	// fewer lines is worth more, which is the whole point of shipping it below
	// 1. At 0 the size stops mattering at all and every PR is a flat fee.
	ChurnExponent    float64  `json:"churn_exponent"`
	DeletionWeight   float64  `json:"deletion_weight"`
	Buckets          []Bucket `json:"buckets"`
	Approved         float64  `json:"approved"`
	Commented        float64  `json:"commented"`
	RequestedChanges float64  `json:"requested_changes"`
	ShrinkBonus      float64  `json:"shrink_bonus"`
	AttemptDecay     float64  `json:"attempt_decay"`
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
	// Curve decides how a bucket's multiplier applies: as a STEP that holds
	// across the whole tier, or as an ANCHOR the multiplier moves smoothly
	// between. Empty means the shipped default, which is linear.
	//
	// So "" and "linear" score alike but hash differently. Harmless: only
	// RESOLVED rules are hashed onto history rows, and resolution always
	// starts from DefaultRules, which names the curve.
	Curve string `json:"curve,omitempty"`
}

// DefaultRules is the shipped policy.
//
// The shape it aims for: the same solve in fewer lines is worth MORE. A
// +100/-100 PR scores 190, a +200/-200 one 158, a +300/-300 one 135. That
// needs ChurnExponent well below 1; at 1 the churn term outruns the falling
// rate and a sprawling PR always wins, which is the opposite incentive.
//
// Base is points per ChurnUnit lines and the multipliers set the RATE that is
// paid at, so the ladder decides which SIZE is paid best (still "small") while
// the exponent decides how fast the total falls away from it.
//
// DeletionWeight above 1 is deliberate: a removed line counts more than an
// added one, so deleting is the cheapest way to earn. It pushes a big deletion
// UP the ladder into a worse rate, which is the point - a PR should not be
// able to farm by deleting indiscriminately - and the shrink bonus is what
// makes the net-negative PR come out ahead anyway.
//
// The ladder is read as a CURVE by default, so those multipliers are the
// points the rate passes through rather than five plateaus with cliffs
// between them. The tiers still name what a PR is, which is what makes a
// score explainable; they just no longer decide it on their own.
func DefaultRules() Rules {
	return Rules{
		Base:           100,
		ChurnUnit:      80,
		ChurnExponent:  0.15,
		DeletionWeight: 1.5,
		Curve:          CurveLinear,
		Buckets: []Bucket{
			{Name: "tiny", MaxChurn: 10, Multiplier: 1.0},
			{Name: "small", MaxChurn: 50, Multiplier: 1.5},
			{Name: "medium", MaxChurn: 250, Multiplier: 1.0},
			{Name: "large", MaxChurn: 1000, Multiplier: 0.5},
			{Name: "huge", Multiplier: 0.2},
		},
		Approved:         1.0,
		Commented:        0.25,
		RequestedChanges: -0.25,
		ShrinkBonus:      1.6,
		AttemptDecay:     0.4,
		UseGitattributes: true,
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
// surprising number explain itself: the same diff scores very differently
// either side of a tier boundary, and the boundary is the part a reader cannot
// infer from the score alone. Churn and net WERE here too and are not, because
// both are arithmetic over figures the history row already carries, so keeping
// them meant three fields that could disagree with each other.
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
	churn := r.Churn(in.Additions, in.Deletions)
	net := in.Additions - in.Deletions
	bucket := bucketFor(r.Buckets, churn)

	res := Result{Bucket: bucket.Name}

	verdictMult, ok := verdictMultiplier(r, in.Verdict)
	if !ok || in.Attempt < 1 || len(r.Buckets) == 0 {
		return res
	}

	// Nothing reviewable, nothing earned.
	//
	// Without this guard a PR whose every file is linguist-generated (a
	// lockfile bump, a regenerated client, this repo's own committed
	// internal/dashboard/assets bundle) reaches churn 0, lands in the smallest
	// bucket AND collects the shrink bonus for a net of 0: 120 points, more
	// than a real +200/-100 PR earns. Exclusion is meant to stop generated
	// lines from burying a change, not to mint points for a PR that contains
	// nothing else. It is scored, and the answer is zero.
	if churn <= 0 {
		return res
	}

	// Scaled by the work, at ChurnExponent. Never a flat fee, and by default
	// not proportional either.
	//
	// A flat fee (exponent 0) is farmable without bound: points then track how
	// many PRs you opened rather than how much was reviewed. Proportional
	// (exponent 1) is the opposite failure for this project's purpose: the
	// churn term outruns any falling rate, so a sprawling PR always beats the
	// same solve written tightly. The shipped 0.15 sits deliberately near the
	// flat end, which buys the incentive at a known cost: points per unit of
	// churn now rise as a PR gets smaller, so chopping work into fragments
	// pays better than shipping it whole. A ladder whose first tier pays 0
	// puts a floor under that, and is the dial to reach for if anybody starts
	// opening one-line PRs.
	points := r.Base * math.Pow(churn/r.ChurnUnit, r.ChurnExponent) * sizeMultiplier(r, bucket, churn) * verdictMult
	// Only ever a bonus. Shrinking the codebase must not AMPLIFY a penalty:
	// without the sign check a rejected deletion is punished 1.2x harder than
	// a rejected addition, which rewards exactly the wrong thing.
	if net <= 0 && points > 0 {
		points *= r.ShrinkBonus
	}
	points *= math.Pow(r.AttemptDecay, float64(in.Attempt-1))

	res.Score = roundHalfAway(points)
	return res
}

// Churn is how much of a PR there is to read: added lines, plus removed ones
// at whatever a removal is worth. It is the figure every other dial is
// applied to, and the one a size tier is chosen by.
//
// Exported so that anything explaining a score (the dashboard's calculator)
// weighs deletions the same way the scorer does, rather than restating
// deletion_weight somewhere it can fall out of step.
func (r Rules) Churn(additions, deletions int) float64 {
	return float64(additions) + float64(deletions)*r.DeletionWeight
}

// Rate is what one unit of churn is paid at, at this size, under this curve.
// The tier is resolved the way Compute resolves it, so a rate quoted to
// somebody is the rate they will be paid.
func (r Rules) Rate(churn float64) float64 {
	return sizeMultiplier(r, bucketFor(r.Buckets, churn), churn)
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
	// INTEGER is 32-bit). The IsInf guard alone was not enough: a base of
	// 1e308 stays FINITE through the multipliers, so nothing tripped, and the
	// int conversion then saturated to 9223372036854775807 on its way into a
	// 32-bit column. Validate bounds base as well; this is the backstop for a
	// config.json edited by hand, which never passes through that check.
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

// bucketFor returns the first bucket whose inclusive MaxChurn covers churn.
// A bucket with MaxChurn 0 is open-ended and matches anything, which is why
// validation insists it can only be the last one. An empty list yields a zero
// bucket, whose 0 multiplier scores nothing.
func bucketFor(buckets []Bucket, churn float64) Bucket {
	for _, b := range buckets {
		if b.MaxChurn <= 0 || churn <= b.MaxChurn {
			return b
		}
	}
	return Bucket{}
}

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
