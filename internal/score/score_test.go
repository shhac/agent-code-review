package score

import "testing"

// The worked examples from the design (.ai-cache/plan-pr-scoring.md), carried
// here verbatim so the documented numbers and the code cannot drift. Each
// scenario is a full PR history, because the interesting cases are about how
// repeated rounds compose, not about one review in isolation.
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
			name: "small approved first pass", additions: 40, deletions: 10,
			rounds:    []round{{verdictApproved, 135}},
			wantTotal: 135, wantBucket: "small",
		},
		{
			name: "medium approved first pass", additions: 200, deletions: 100,
			rounds:    []round{{verdictApproved, 500}},
			wantTotal: 500, wantBucket: "medium",
		},
		{
			name: "small, two comment rounds then approved", additions: 40, deletions: 10,
			rounds:    []round{{verdictCommented, 34}, {verdictCommented, 20}, {verdictApproved, 49}},
			wantTotal: 103, wantBucket: "small",
		},
		{
			name: "huge, two comment rounds then approved", additions: 2000, deletions: 0,
			rounds:    []round{{verdictCommented, 200}, {verdictCommented, 120}, {verdictApproved, 288}},
			wantTotal: 608, wantBucket: "huge",
		},
		{
			name: "pure deletion approved first pass", additions: 0, deletions: 800,
			rounds:    []round{{verdictApproved, 480}},
			wantTotal: 480, wantBucket: "large",
		},
		{
			name: "small, rejected then approved", additions: 40, deletions: 10,
			rounds:    []round{{verdictRequestedChanges, -34}, {verdictApproved, 81}},
			wantTotal: 47, wantBucket: "small",
		},
		{
			name: "small, rejected then abandoned", additions: 40, deletions: 10,
			rounds:    []round{{verdictRequestedChanges, -34}},
			wantTotal: -34, wantBucket: "small",
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

// THE farming test, and the one the original got wrong.
//
// Comparing one tiny PR to one small PR proved nothing: the attack is VOLUME.
// With a flat fee per PR, points tracked how many PRs you opened rather than
// how much was reviewed, so chopping a change into ever-smaller pieces
// multiplied the payout without bound (2,000 lines as 200 ten-line PRs scored
// 1000x the same change shipped whole). Scaling by churn caps what any
// decomposition can gain at the spread between the best and worst rates.
func TestDecompositionIsRewardedButBounded(t *testing.T) {
	r := DefaultRules()
	total := func(lines, chunk int) int {
		per := Compute(r, Input{Additions: chunk, Verdict: verdictApproved, Attempt: 1}).Score
		return per * (lines / chunk)
	}

	for _, lines := range []int{2000, 20000} {
		whole := total(lines, lines)
		wellSized := total(lines, 50)
		chunky := total(lines, 1000)
		fragments := total(lines, 10)

		// Splitting a big change into reviewable pieces SHOULD pay.
		if chunky <= whole {
			t.Errorf("%d lines: %d as chunks of 1000 vs %d whole; splitting must be rewarded", lines, chunky, whole)
		}
		// The sweet spot is the small bucket, not the smallest possible piece.
		if fragments >= wellSized {
			t.Errorf("%d lines: fragments scored %d against well-sized %d; fragmenting must not be optimal",
				lines, fragments, wellSized)
		}
		// And the whole incentive is bounded by the multiplier spread.
		best := max(max(whole, wellSized), max(chunky, fragments))
		if ratio := float64(best) / float64(whole); ratio > 8 {
			t.Errorf("%d lines: best decomposition is %.0fx one PR; must stay near the 7.5x multiplier spread", lines, ratio)
		}
	}
}

// Bigger PRs earn more in total (more work) but at a worse rate per line.
func TestBiggerPRsEarnMoreInTotalAndLessPerLine(t *testing.T) {
	r := DefaultRules()
	rate := func(lines int) float64 {
		return float64(Compute(r, Input{Additions: lines, Verdict: verdictApproved, Attempt: 1}).Score) / float64(lines)
	}
	small := Compute(r, Input{Additions: 50, Verdict: verdictApproved, Attempt: 1}).Score
	huge := Compute(r, Input{Additions: 5000, Verdict: verdictApproved, Attempt: 1}).Score
	if huge <= small {
		t.Errorf("a 5000-line PR scored %d against a 50-line PR's %d: more work should earn more", huge, small)
	}
	if rate(5000) >= rate(50) {
		t.Errorf("per-line rate %v at 5000 lines vs %v at 50: the big PR must be paid worse per line", rate(5000), rate(50))
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
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

// Deletions are discounted in churn (cheaper to read) AND earn the shrink
// bonus when the PR is net-negative. Both express "removing code is good", so
// a pure deletion must beat the same volume of additions.
func TestDeletionsBeatAdditionsOfTheSameVolume(t *testing.T) {
	r := DefaultRules()
	added := Compute(r, Input{Additions: 400, Verdict: verdictApproved, Attempt: 1}).Score
	removed := Compute(r, Input{Deletions: 400, Verdict: verdictApproved, Attempt: 1}).Score
	if removed <= added {
		t.Errorf("removing 400 lines scored %d, adding 400 scored %d: removal must score higher", removed, added)
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
	// -40 net against +40 net, both rejected, both landing in the same bucket
	// (churn 20 vs 40 are each "small"), so the bonus is the only difference.
	shrinking := Compute(r, Input{Additions: 0, Deletions: 40, Verdict: verdictRequestedChanges, Attempt: 1})
	growing := Compute(r, Input{Additions: 40, Deletions: 0, Verdict: verdictRequestedChanges, Attempt: 1})
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
