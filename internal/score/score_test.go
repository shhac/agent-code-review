package score

import "testing"

// The worked examples, carried here so the documented numbers and the code
// cannot drift. Each scenario is a full PR history, because the interesting
// cases are about how repeated rounds compose, not about one review in
// isolation.
func TestWorkedExamples(t *testing.T) {
	r := DefaultRules()

	type round struct {
		verdict string
		want    int
	}
	cases := []struct {
		name       string
		additions  int
		deletions  int
		rounds     []round
		wantTotal  int
		wantBucket string
	}{
		{
			name: "the best a single PR can do", additions: 100, deletions: 100,
			rounds:    []round{{verdictApproved, 100}},
			wantTotal: 100, wantBucket: "medium",
		},
		{
			name: "the same solve at ten times the length", additions: 1000, deletions: 1000,
			rounds:    []round{{verdictApproved, 29}},
			wantTotal: 29, wantBucket: "huge",
		},
		{
			name: "mostly removal", additions: 100, deletions: 1000,
			rounds:    []round{{verdictApproved, 227}},
			wantTotal: 227, wantBucket: "huge",
		},
		{
			name: "mostly addition, identical line count", additions: 1000, deletions: 100,
			rounds:    []round{{verdictApproved, 47}},
			wantTotal: 47, wantBucket: "huge",
		},
		{
			name: "a big deletion", additions: 0, deletions: 2000,
			rounds:    []round{{verdictApproved, 429}},
			wantTotal: 429, wantBucket: "huge",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			total := 0
			for i, rd := range tc.rounds {
				attempt := i + 1
				got := Compute(r, Input{
					Additions: tc.additions, Deletions: tc.deletions,
					Verdict: rd.verdict, Attempt: attempt,
				})
				if got.Score != rd.want {
					t.Errorf("attempt %d (%s): score = %d, want %d", attempt, rd.verdict, got.Score, rd.want)
				}
				if got.Bucket != tc.wantBucket {
					t.Errorf("attempt %d: bucket = %q, want %q", attempt, got.Bucket, tc.wantBucket)
				}
				total += got.Score
			}
			if total != tc.wantTotal {
				t.Errorf("total = %d, want %d", total, tc.wantTotal)
			}
		})
	}
}

// The requirement that started this: rounds of comments ending in an approval
// must total LESS than getting it right first time, or the incentive points
// the wrong way.
func TestCommentRoundsBeatenByFirstPassApproval(t *testing.T) {
	r := DefaultRules()

	first := Compute(r, Input{Additions: 100, Deletions: 100, Verdict: verdictApproved, Attempt: 1}).Score

	slow := 0
	for i, v := range []string{verdictCommented, verdictCommented, verdictApproved} {
		slow += Compute(r, Input{Additions: 100, Deletions: 100, Verdict: v, Attempt: i + 1}).Score
	}

	if slow >= first {
		t.Errorf("comment rounds then approve scored %d, first-pass approve %d: must be lower", slow, first)
	}
}

// THE property this ruleset exists for, in the owner's own terms: the same
// solve in fewer lines is worth more, and a PR that mostly removes beats one
// that mostly adds.
//
// Pinned as numbers rather than as an ordering. An ordering would still pass
// if the curve flattened to within a point of itself, and a leaderboard nobody
// can feel is not an incentive.
func TestTheShapeOfAGoodPullRequest(t *testing.T) {
	r := DefaultRules()
	at := func(a, d int) int {
		return Compute(r, Input{Additions: a, Deletions: d, Verdict: verdictApproved, Attempt: 1}).Score
	}
	for _, tc := range []struct {
		a, d, want int
	}{
		{100, 100, 100},  // the peak: the best a single PR can do
		{1000, 1000, 29}, // the same solve, ten times the length
		{100, 1000, 227}, // mostly removal
		{1000, 100, 47},  // mostly addition, identical line count
		{5, 1000, 250},   // almost pure removal
		{1000, 5, 51},    // almost pure addition
	} {
		if got := at(tc.a, tc.d); got != tc.want {
			t.Errorf("+%d/-%d scored %d, want %d", tc.a, tc.d, got, tc.want)
		}
	}
	if at(100, 100) <= at(1000, 1000) {
		t.Error("the same solve in fewer lines must be worth more")
	}
	if at(100, 1000) <= at(1000, 100) {
		t.Error("a PR that mostly removes must beat one that mostly adds")
	}
}

// Removing MORE must always earn more. The previous ruleset ran deletions
// through the same curve that punishes size, so deleting 2000 lines earned
// less than deleting 10: the shape that makes a tighter solve win is exactly
// wrong for a deletion, which is not a solve but the outcome.
func TestRemovingMoreAlwaysEarnsMore(t *testing.T) {
	r := DefaultRules()
	at := func(d int) int {
		return Compute(r, Input{Deletions: d, Verdict: verdictApproved, Attempt: 1}).Score
	}
	prev := at(1)
	for d := 2; d <= 100000; d++ {
		got := at(d)
		if got < prev {
			t.Fatalf("-%d scored %d against %d for one line less: removing more must never earn less", d, got, prev)
		}
		prev = got
	}
	if at(2000) <= at(200) {
		t.Errorf("-2000 scored %d against -200's %d; the gap should be substantial", at(2000), at(200))
	}
}

// Past the peak a bigger PR earns less, every line of the way out. That is the
// whole point of the falloff, and the property that inverts if somebody sets
// it to 2 or below.
func TestPastThePeakBiggerEarnsLess(t *testing.T) {
	r := DefaultRules()
	at := func(changed int) int {
		return Compute(r, Input{Additions: changed, Verdict: verdictApproved, Attempt: 1}).Score
	}
	peak := int(r.Peak())
	if peak != 200 {
		t.Fatalf("peak = %d, want the 200 the shipped dials put it at", peak)
	}
	prev := at(peak)
	for changed := peak + 1; changed <= 20000; changed++ {
		got := at(changed)
		if got > prev {
			t.Fatalf("+%d scored %d against %d one line smaller: past the peak, bigger must not earn more", changed, got, prev)
		}
		prev = got
	}
}

// Granular beats monolithic, and atomised loses to both.
//
// This is the property the previous ruleset could not hold at the same time as
// the one above. Points per line rose without limit as a PR shrank, so a
// hundred one-line PRs beat everything, and the only defence was a tier that
// paid nothing. Here the reward is quadratic near zero, so fragments collapse
// on their own.
func TestGranularBeatsMonolithicButAtomisedLosesToBoth(t *testing.T) {
	r := DefaultRules()
	split := func(lines, piece int) int {
		per := Compute(r, Input{Additions: piece, Verdict: verdictApproved, Attempt: 1}).Score
		whole := lines / piece
		total := whole * per
		if rest := lines - whole*piece; rest > 0 {
			total += Compute(r, Input{Additions: rest, Verdict: verdictApproved, Attempt: 1}).Score
		}
		return total
	}

	const lines = 2000
	monolith := split(lines, lines)
	granular := split(lines, int(r.PieceLines))
	atomised := split(lines, 1)

	if granular <= monolith {
		t.Errorf("%d lines as pieces of %v scored %d against %d shipped whole: granular must pay",
			lines, r.PieceLines, granular, monolith)
	}
	if atomised >= monolith {
		t.Errorf("%d lines as one-line PRs scored %d against %d shipped whole: atomising must not pay",
			lines, atomised, monolith)
	}
	// And the best piece size really is the configured one, not something
	// smaller that happens to sneak past.
	for _, piece := range []int{1, 2, 5, 10, 25, 100, 200, 500} {
		if got := split(lines, piece); got > granular {
			t.Errorf("pieces of %d scored %d, beating the configured piece size's %d", piece, got, granular)
		}
	}
}

// The two landmarks land exactly where the dials say, for any falloff. They
// are what the whole policy is explained in terms of, so a normalisation
// constant baked in for one falloff would quietly move them for every other.
func TestTheLandmarksLandOnTheDials(t *testing.T) {
	for _, falloff := range []float64{2.5, 3, 4, 6} {
		r := DefaultRules()
		r.SizeFalloff = falloff

		bestPer, perAt := 0.0, 0.0
		bestTotal, totalAt := 0.0, 0.0
		for changed := 1.0; changed <= 20000; changed++ {
			v := r.SizeReward(changed)
			if v/changed > bestPer {
				bestPer, perAt = v/changed, changed
			}
			if v > bestTotal {
				bestTotal, totalAt = v, changed
			}
		}
		if perAt != r.PieceLines {
			t.Errorf("falloff %v: points per line peak at %v changed lines, want piece_lines %v", falloff, perAt, r.PieceLines)
		}
		if totalAt < r.Peak()-1 || totalAt > r.Peak()+1 {
			t.Errorf("falloff %v: points per PR peak at %v, want Peak() %v", falloff, totalAt, r.Peak())
		}
		if bestTotal < r.SizePoints-1 || bestTotal > r.SizePoints {
			t.Errorf("falloff %v: the peak pays %v, want size_points %v", falloff, bestTotal, r.SizePoints)
		}
		_ = bestPer
	}
}

// The tier is a LABEL now. It has to keep naming the same landmarks the dials
// name, or a history row explains a score against boundaries nothing uses.
func TestTiersNameThePolicysOwnLandmarks(t *testing.T) {
	r := DefaultRules()
	for _, tc := range []struct {
		changed int
		want    string
	}{
		{1, "tiny"}, {12, "tiny"}, {13, "small"}, {50, "small"},
		{51, "medium"}, {200, "medium"}, {201, "large"}, {1000, "large"}, {1001, "huge"},
	} {
		if got := r.Tier(float64(tc.changed)); got != tc.want {
			t.Errorf("%d changed lines: tier %q, want %q", tc.changed, got, tc.want)
		}
	}
	// And they move with the dials rather than being a second set of numbers.
	r.PieceLines = 100
	if got := r.Tier(80); got != "small" {
		t.Errorf("with piece_lines 100, 80 changed lines is %q, want small", got)
	}
}

// The tier reads CHANGED lines, never the net. The original sketch bucketed on
// net, which files a +5000/-4900 PR as tiny.
func TestTierUsesChangedLinesNotNet(t *testing.T) {
	r := DefaultRules()
	got := Compute(r, Input{Additions: 5000, Deletions: 4900, Verdict: verdictApproved, Attempt: 1})
	if got.Bucket != "huge" {
		t.Errorf("+5000/-4900 is %q, want huge: 9900 lines were reviewed", got.Bucket)
	}
}

// Removing beats adding at the same line count, and the size reward is blind
// to the direction: the removal reward is the entire difference.
func TestDeletionsBeatAdditions(t *testing.T) {
	r := DefaultRules()
	at := func(a, d int) int {
		return Compute(r, Input{Additions: a, Deletions: d, Verdict: verdictApproved, Attempt: 1}).Score
	}
	removed, added := at(0, 300), at(300, 0)
	if removed <= added {
		t.Errorf("removing 300 scored %d, adding 300 scored %d: removal must win", removed, added)
	}
	if gap := removed - added; gap != roundHalfAway(r.RemovalReward(300)) {
		t.Errorf("the gap is %d, want exactly the removal reward (%d)", gap, roundHalfAway(r.RemovalReward(300)))
	}
}

// A pure move earns nothing extra: it did not make the codebase smaller, and
// the review it did cost is already paid for by the size reward.
func TestAPureMoveEarnsNoRemovalReward(t *testing.T) {
	r := DefaultRules()
	move := Compute(r, Input{Additions: 250, Deletions: 250, Verdict: verdictApproved, Attempt: 1}).Score
	if want := roundHalfAway(r.SizeReward(500)); move != want {
		t.Errorf("+250/-250 scored %d, want the size reward alone (%d)", move, want)
	}
}

// SKIPPED and ERROR are outcomes of our own machinery, not feedback to the
// author. They must never cost anybody points.
func TestNonRealVerdictsScoreNothing(t *testing.T) {
	r := DefaultRules()
	for _, v := range []string{"SKIPPED", "ERROR", "WORKING", ""} {
		if got := Compute(r, Input{Additions: 100, Deletions: 100, Verdict: v, Attempt: 1}); got.Score != 0 {
			t.Errorf("verdict %q scored %d, want 0", v, got.Score)
		}
	}
	// Case and padding are tolerated, because a verdict arrives as a string.
	if got := Compute(r, Input{Additions: 100, Deletions: 100, Verdict: " approved ", Attempt: 1}); got.Score == 0 {
		t.Error("a padded, lower-case APPROVED scored nothing")
	}
}

// Compute is total: it is called right after a review that has already been
// paid for, so there is nothing useful a caller could do with an error.
func TestComputeIsTotal(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    Rules
		in   Input
	}{
		{"zero rules", Rules{}, Input{Additions: 10, Verdict: verdictApproved, Attempt: 1}},
		{"no falloff", Rules{SizePoints: 100, PieceLines: 50, Approved: 1, AttemptDecay: 0.4},
			Input{Additions: 10, Verdict: verdictApproved, Attempt: 1}},
		{"attempt zero", DefaultRules(), Input{Additions: 10, Verdict: verdictApproved, Attempt: 0}},
		{"negative counts", DefaultRules(), Input{Additions: -5, Deletions: -5, Verdict: verdictApproved, Attempt: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Compute(tc.r, tc.in); got.Score != 0 {
				t.Errorf("score = %d, want 0", got.Score)
			}
		})
	}
}

// Decay must actually decay, for every real verdict, without flipping sign.
func TestAttemptDecayMonotonic(t *testing.T) {
	r := DefaultRules()
	for _, v := range []string{verdictApproved, verdictCommented, verdictRequestedChanges} {
		prev := Compute(r, Input{Additions: 100, Deletions: 100, Verdict: v, Attempt: 1}).Score
		for attempt := 2; attempt <= 5; attempt++ {
			got := Compute(r, Input{Additions: 100, Deletions: 100, Verdict: v, Attempt: attempt}).Score
			if abs(got) > abs(prev) {
				t.Errorf("%s attempt %d scored %d, more than attempt %d's %d", v, attempt, got, attempt-1, prev)
			}
			if got != 0 && (got > 0) != (prev > 0) {
				t.Errorf("%s attempt %d flipped sign: %d after %d", v, attempt, got, prev)
			}
			prev = got
		}
	}
}

func TestRoundHalfAwayFromZero(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want int
	}{{0.5, 1}, {-0.5, -1}, {1.4, 1}, {-1.4, -1}, {2.5, 3}, {-2.5, -3}} {
		if got := roundHalfAway(tc.in); got != tc.want {
			t.Errorf("roundHalfAway(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// An empty diff earns nothing, however it arose: an empty PR, or one whose
// every line was excluded as generated. Guards the hole where a lockfile bump
// with nothing else in it collects points for being small.
func TestNothingReviewableScoresNothing(t *testing.T) {
	r := DefaultRules()
	for _, v := range []string{verdictApproved, verdictCommented, verdictRequestedChanges} {
		if got := Compute(r, Input{Verdict: v, Attempt: 1}); got.Score != 0 {
			t.Errorf("verdict %s on an empty diff scored %d, want 0", v, got.Score)
		}
	}
}

// Removing code is a reward, never an amplifier on a penalty. Without the
// clamp a rejected deletion is punished harder than a rejected addition of the
// same size, rewarding exactly the wrong behaviour.
func TestRemovalRewardNeverDeepensAPenalty(t *testing.T) {
	r := DefaultRules()
	// Matched on changed lines, so the size reward is identical and the
	// removal reward is the only thing that could separate them.
	shrinking := Compute(r, Input{Deletions: 300, Verdict: verdictRequestedChanges, Attempt: 1})
	growing := Compute(r, Input{Additions: 300, Verdict: verdictRequestedChanges, Attempt: 1})
	if shrinking.Score != growing.Score {
		t.Errorf("rejected shrinking PR scored %d, rejected growing PR %d: the removal reward must not touch a penalty",
			shrinking.Score, growing.Score)
	}
	if shrinking.Score >= 0 {
		t.Fatalf("rejected PR scored %d; this test no longer demonstrates the hazard", shrinking.Score)
	}
}

// abs is the decay test's own arithmetic.
func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}
