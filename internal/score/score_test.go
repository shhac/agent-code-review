package score

import "testing"

// The worked examples, carried here so the documented numbers and the code
// cannot drift. Each scenario is a full PR history, because the interesting
// cases are about how repeated rounds compose, not about one review in
// isolation.
//
// The figures have moved twice. First when the ladder became a curve, so a
// tier's multiplier is the rate at its own boundary rather than a plateau.
// Then when the policy stopped being proportional: with a churn exponent of
// 0.15 a PR's size barely lifts its score, and the ladder's falling rate is
// what decides it, so the whole table compressed and the big scenarios
// collapsed. A 2000-line PR now earns less than a 50-line one, which is the
// entire point. Deletions weighing 1.5 also moved every bucket: +40/-10 is 55
// churn, which is "medium" and no longer "small".
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
			name: "tidy change approved first pass", additions: 40, deletions: 10,
			rounds:    []round{{verdictApproved, 139}},
			wantTotal: 139, wantBucket: "medium",
		},
		{
			name: "three times the change, two thirds the points", additions: 200, deletions: 100,
			rounds:    []round{{verdictApproved, 110}},
			wantTotal: 110, wantBucket: "large",
		},
		{
			name: "tidy change, two comment rounds then approved", additions: 40, deletions: 10,
			rounds:    []round{{verdictCommented, 35}, {verdictCommented, 14}, {verdictApproved, 22}},
			wantTotal: 71, wantBucket: "medium",
		},
		{
			name: "huge, two comment rounds then approved", additions: 2000, deletions: 0,
			rounds:    []round{{verdictCommented, 14}, {verdictCommented, 6}, {verdictApproved, 9}},
			wantTotal: 29, wantBucket: "huge",
		},
		{
			name: "pure deletion approved first pass", additions: 0, deletions: 800,
			rounds:    []round{{verdictApproved, 111}},
			wantTotal: 111, wantBucket: "huge",
		},
		{
			name: "tidy change, rejected then approved", additions: 40, deletions: 10,
			rounds:    []round{{verdictRequestedChanges, -35}, {verdictApproved, 56}},
			wantTotal: 21, wantBucket: "medium",
		},
		{
			name: "tidy change, rejected then abandoned", additions: 40, deletions: 10,
			rounds:    []round{{verdictRequestedChanges, -35}},
			wantTotal: -35, wantBucket: "medium",
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
	in := Input{Additions: 40, Deletions: 10}

	first := Compute(r, Input{Additions: in.Additions, Deletions: in.Deletions, Verdict: verdictApproved, Attempt: 1}).Score

	slow := 0
	for i, v := range []string{verdictCommented, verdictCommented, verdictApproved} {
		slow += Compute(r, Input{Additions: in.Additions, Deletions: in.Deletions, Verdict: v, Attempt: i + 1}).Score
	}

	if slow >= first {
		t.Errorf("comment rounds then approve scored %d, first-pass approve %d: must be lower", slow, first)
	}
}

// Bucketing reads CHURN, never the net. The original sketch bucketed on net,
// which files a +5000/-4900 PR as "small".
func TestBucketUsesChurnNotNet(t *testing.T) {
	r := DefaultRules()
	got := Compute(r, Input{Additions: 5000, Deletions: 4900, Verdict: verdictApproved, Attempt: 1})
	if got.Bucket != "huge" {
		t.Errorf("bucket = %q, want huge: +5000/-4900 is a huge review, not a small one", got.Bucket)
	}
}

// Splitting a big change into reviewable pieces pays, and how much it pays is
// now the operator's problem rather than a guarantee.
//
// Under proportional scoring the ceiling was the ladder's rate spread: 7.5x,
// whatever anybody did. A churn exponent below 1 removes that ceiling by
// construction, because points per unit of churn now RISE as a PR gets
// smaller. At the shipped 0.15, two thousand lines shipped whole earn 57 and
// the same lines as two thousand one-line PRs earn 104,000. That is the price
// of "the same solve in fewer lines is worth more", and it is a price rather
// than a bug: the two properties are the same statement with the sign flipped.
//
// The dial that puts a floor under it is a first tier that pays nothing, so a
// PR too small to be worth reviewing is worth no points either. This test
// pins both halves: the incentive to split, and the dial that stops it running
// away.
func TestFragmentingIsFlooredByAZeroRatedFirstTier(t *testing.T) {
	split := func(r Rules, lines, chunk int) int {
		per := Compute(r, Input{Additions: chunk, Verdict: verdictApproved, Attempt: 1}).Score
		return per * (lines / chunk)
	}

	r := DefaultRules()
	if whole, chunky := split(r, 2000, 2000), split(r, 2000, 200); chunky <= whole {
		t.Errorf("2000 lines: %d as ten pieces vs %d whole; splitting into reviewable pieces must pay", chunky, whole)
	}
	if fragments, wellSized := split(r, 2000, 1), split(r, 2000, 50); fragments <= wellSized {
		t.Errorf("fragments scored %d against well-sized %d; the shipped ruleset is expected to be farmable this way, "+
			"so if this now holds the floor arrived somewhere and this test should say where", fragments, wellSized)
	}

	floored := DefaultRules()
	floored.Buckets = append([]Bucket(nil), floored.Buckets...)
	floored.Buckets[0] = Bucket{Name: "tiny", MaxChurn: 10, Multiplier: 0}
	if fragments, wellSized := split(floored, 2000, 1), split(floored, 2000, 50); fragments >= wellSized {
		t.Errorf("with a zero-rated first tier, fragments still scored %d against well-sized %d", fragments, wellSized)
	}
}

// THE property this ruleset exists for: the same solve in fewer lines is
// worth more.
//
// The three figures are the ones the policy was tuned against, so they are
// pinned as numbers rather than as an ordering: an ordering would still pass
// if the curve flattened to within a point of itself, and a leaderboard
// nobody can feel is not an incentive.
func TestATighterSolveEarnsMore(t *testing.T) {
	r := DefaultRules()
	for _, tc := range []struct {
		lines int
		want  int
	}{{100, 190}, {200, 158}, {300, 135}} {
		got := Compute(r, Input{Additions: tc.lines, Deletions: tc.lines, Verdict: verdictApproved, Attempt: 1}).Score
		if got != tc.want {
			t.Errorf("+%d/-%d scored %d, want %d", tc.lines, tc.lines, got, tc.want)
		}
	}
}

// Past the ladder's peak, a bigger PR earns less in total, all the way out.
//
// This is the inverse of what the ruleset guaranteed until the churn exponent
// arrived, and the inversion is deliberate rather than a side effect: the
// tail-anchor rule that used to enforce the old direction now stands aside
// when the exponent says the decline is the policy.
func TestPastThePeakBiggerEarnsLess(t *testing.T) {
	r := DefaultRules()
	at := func(lines int) int {
		return Compute(r, Input{Additions: lines, Deletions: lines, Verdict: verdictApproved, Attempt: 1}).Score
	}
	peak, peakAt := 0, 0
	for n := 1; n <= 3000; n++ {
		if v := at(n); v > peak {
			peak, peakAt = v, n
		}
	}
	if peakAt > 60 {
		t.Errorf("the best-paid PR is +%d/-%d; the peak should sit at a small change, not a large one", peakAt, peakAt)
	}
	// Out to the ladder's final anchor, which at 4000 churn is 1600 lines each
	// way. Past it the rate is flat, because interpolation has nothing beyond
	// the last anchor to aim at, so the churn term creeps the total back up.
	// The creep is real and deliberately tolerated: it runs from 57 points to
	// about 70 over the following sixteen thousand lines, which is a third of
	// what the peak pays and not an incentive anybody can act on. The second
	// assertion is what keeps it that way.
	prev := peak
	for n := peakAt + 1; n <= 1600; n++ {
		got := at(n)
		if got > prev {
			t.Fatalf("+%d/-%d scored %d against %d one line smaller: past the peak, bigger must not earn more", n, n, got, prev)
		}
		prev = got
	}
	if huge := at(20000); huge >= peak/2 {
		t.Errorf("+20000/-20000 scored %d against a peak of %d: the flat tail must stay far below the peak", huge, peak)
	}
}

func TestBucketBoundariesAreInclusive(t *testing.T) {
	r := DefaultRules()
	// deletion_weight 0.5, so additions alone make churn exact.
	for _, tc := range []struct {
		additions int
		want      string
	}{
		{10, "tiny"}, {11, "small"}, {50, "small"}, {51, "medium"},
		{250, "medium"}, {251, "large"}, {1000, "large"}, {1001, "huge"},
	} {
		got := Compute(r, Input{Additions: tc.additions, Verdict: verdictApproved, Attempt: 1})
		if got.Bucket != tc.want {
			t.Errorf("%d additions: bucket = %q, want %q", tc.additions, got.Bucket, tc.want)
		}
	}
}

// Removing code beats adding it, at the same churn AND at the same line count.
//
// Both hold now, by two different mechanisms, and it is worth keeping them
// apart. At the same CHURN the shrink bonus is the whole difference. At the
// same LINE COUNT a deletion measures as MORE churn (weight 1.5), which pushes
// it up the ladder into a worse rate: the near-flat churn exponent means that
// costs it very little, and the bonus more than covers it. Under the old
// proportional ruleset the same weighting would have made a big deletion the
// best-paid PR on the board, which is why it was below 1 then.
func TestDeletionsBeatAdditions(t *testing.T) {
	r := DefaultRules()
	sameChurn := func() (int, int) {
		added := Compute(r, Input{Additions: 300, Verdict: verdictApproved, Attempt: 1})
		removed := Compute(r, Input{Deletions: 200, Verdict: verdictApproved, Attempt: 1})
		if added.Bucket != removed.Bucket {
			t.Fatalf("buckets differ (%q vs %q); the two are no longer the same amount of reviewing", added.Bucket, removed.Bucket)
		}
		return removed.Score, added.Score
	}
	if removed, added := sameChurn(); removed <= added {
		t.Errorf("removing 200 lines scored %d, adding the same 300 churn scored %d: removal must score higher", removed, added)
	}
	removed := Compute(r, Input{Deletions: 300, Verdict: verdictApproved, Attempt: 1}).Score
	added := Compute(r, Input{Additions: 300, Verdict: verdictApproved, Attempt: 1}).Score
	if removed <= added {
		t.Errorf("removing 300 lines scored %d, adding 300 scored %d: removal must score higher", removed, added)
	}
}

// A net-zero PR (a pure move or rename) counts as not-growing.
func TestNetZeroEarnsTheShrinkBonus(t *testing.T) {
	r := DefaultRules()
	got := Compute(r, Input{Additions: 20, Deletions: 20, Verdict: verdictApproved, Attempt: 1})
	plain := Compute(r, Input{Additions: 30, Deletions: 0, Verdict: verdictApproved, Attempt: 1})
	// Same bucket (churn 30 both), so the only difference is the bonus.
	if got.Bucket != plain.Bucket {
		t.Fatalf("buckets differ (%q vs %q); test no longer isolates the bonus", got.Bucket, plain.Bucket)
	}
	if got.Score <= plain.Score {
		t.Errorf("net-zero scored %d, net-positive %d: net<=0 must earn the bonus", got.Score, plain.Score)
	}
}

// SKIPPED and ERROR are outcomes of our own machinery, not feedback to the
// author. They must never cost anybody points.
func TestNonRealVerdictsScoreNothing(t *testing.T) {
	r := DefaultRules()
	for _, v := range []string{"SKIPPED", "ERROR", "WORKING", "", "nonsense"} {
		got := Compute(r, Input{Additions: 40, Deletions: 10, Verdict: v, Attempt: 1})
		if got.Score != 0 {
			t.Errorf("verdict %q: scored %d, want 0", v, got.Score)
		}
	}
	// The real verdicts still score, so the guard above is about the verdict
	// and not about the inputs happening to be worthless.
	for _, v := range []string{"APPROVED", "approved", " Commented ", "REQUESTED_CHANGES"} {
		if Compute(r, Input{Additions: 40, Deletions: 10, Verdict: v, Attempt: 1}).Score == 0 {
			t.Errorf("verdict %q should score something", v)
		}
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
		{"no buckets", Rules{Base: 100, Approved: 1, AttemptDecay: 0.6}, Input{Verdict: verdictApproved, Attempt: 1}},
		{"attempt zero", DefaultRules(), Input{Additions: 10, Verdict: verdictApproved}},
		{"negative attempt", DefaultRules(), Input{Additions: 10, Verdict: verdictApproved, Attempt: -3}},
		{"negative counts", DefaultRules(), Input{Additions: -5, Deletions: -5, Verdict: verdictApproved, Attempt: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Compute(tc.r, tc.in) // must not panic
			_ = got
		})
	}
}

// Decay must actually decay, for every real verdict, without flipping sign.
func TestAttemptDecayMonotonic(t *testing.T) {
	r := DefaultRules()
	for _, v := range []string{verdictApproved, verdictCommented, verdictRequestedChanges} {
		prev := Compute(r, Input{Additions: 400, Verdict: v, Attempt: 1}).Score
		for attempt := 2; attempt <= 6; attempt++ {
			got := Compute(r, Input{Additions: 400, Verdict: v, Attempt: attempt}).Score
			if abs(got) > abs(prev) {
				t.Errorf("verdict %s attempt %d: |%d| > |%d|, decay must not increase magnitude", v, attempt, got, prev)
			}
			if prev != 0 && got != 0 && (got > 0) != (prev > 0) {
				t.Errorf("verdict %s attempt %d: sign flipped (%d -> %d)", v, attempt, prev, got)
			}
			prev = got
		}
	}
}

func TestRoundHalfAwayFromZero(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want int
	}{{37.5, 38}, {-37.5, -38}, {22.5, 23}, {-22.5, -23}, {7.2, 7}, {0, 0}, {-0.4, 0}} {
		if got := roundHalfAway(tc.in); got != tc.want {
			t.Errorf("roundHalfAway(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// Churn 0 earns nothing, however it arose: an empty PR, or one whose every
// line was excluded as generated. Guards the farming hole where churn 0 lands
// in the smallest bucket and a net of 0 also collects the shrink bonus.
func TestZeroChurnScoresNothing(t *testing.T) {
	r := DefaultRules()
	for _, v := range []string{verdictApproved, verdictCommented, verdictRequestedChanges} {
		got := Compute(r, Input{Additions: 0, Deletions: 0, Verdict: v, Attempt: 1})
		if got.Score != 0 {
			t.Errorf("verdict %s: zero churn scored %d, want 0", v, got.Score)
		}
	}
	real := Compute(r, Input{Additions: 200, Deletions: 100, Verdict: verdictApproved, Attempt: 1})
	empty := Compute(r, Input{Verdict: verdictApproved, Attempt: 1})
	if empty.Score >= real.Score {
		t.Errorf("an empty PR (%d) must not beat a real one (%d)", empty.Score, real.Score)
	}
}

// Shrinking the codebase is a BONUS, never an amplifier on a penalty. Without
// the sign check a rejected deletion is punished 1.2x harder than a rejected
// addition of the same size, rewarding exactly the wrong behaviour.
func TestShrinkBonusNeverAmplifiesAPenalty(t *testing.T) {
	r := DefaultRules()
	// Matched on CHURN, not on line count: 30 removed lines weigh the same 45
	// as 45 added ones, so both land on the same rate in the same bucket and
	// the shrink bonus is the only thing that could separate them. It must
	// not, because the score is negative.
	shrinking := Compute(r, Input{Additions: 0, Deletions: 30, Verdict: verdictRequestedChanges, Attempt: 1})
	growing := Compute(r, Input{Additions: 45, Deletions: 0, Verdict: verdictRequestedChanges, Attempt: 1})
	if shrinking.Bucket != growing.Bucket {
		t.Fatalf("buckets differ (%q vs %q); test no longer isolates the bonus", shrinking.Bucket, growing.Bucket)
	}
	if shrinking.Score < growing.Score {
		t.Errorf("rejected shrinking PR scored %d, rejected growing PR %d: the bonus must not deepen a penalty",
			shrinking.Score, growing.Score)
	}
}

// abs is the decay test's own arithmetic. It lived in score.go with a comment
// admitting it was a test helper; it belongs here.
func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}
