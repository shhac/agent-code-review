// Pure derivations for the Leaderboard page.
//
// The SCORE itself is never computed here: Go owns that (internal/score), so
// the CLI, the scheduler and this page cannot disagree about what a review was
// worth. What lives here is presentation — ranking ties, bar widths, the
// approval share — and it is separated from the component so it can be tested
// without mounting one.

import type { LeaderboardEntry, LeaderSort, ScoringMode } from './types';

/** A medal for the top three, nothing below. */
export function medal(rank: number): string {
  return ['🥇', '🥈', '🥉'][rank - 1] ?? '';
}

/**
 * Bar width as a percentage of the leader's total.
 *
 * Scaled against the leader rather than the full negative-to-positive span:
 * one badly-scored author would otherwise squash everybody else's bar toward
 * the middle. A negative total gets no bar at all, which is the honest
 * rendering of "has lost points" rather than a bar pointing the wrong way.
 *
 * Takes the scalar, not the collection: the component hoists it once with
 * `maxOf` (the same shape Metrics and ActivityChart already use for their
 * scales) instead of this recomputing a max per rendered row.
 */
export function barWidth(total: number, top: number): number {
  if (top <= 0 || total <= 0) return 0;
  return Math.round((total / top) * 100);
}

/** Share of an author's scored reviews that were approvals, 0-100. */
export function approvalRate(entry: LeaderboardEntry): number {
  if (entry.reviews <= 0) return 0;
  return Math.round((entry.approvals / entry.reviews) * 100);
}

/**
 * Mean points per scored review, to one decimal place.
 *
 * Over `reviews`, which counts only the rows that contributed to the total: a
 * review whose score could not be computed is absent from the sum, so counting
 * it here would understate everybody who has one.
 */
export function meanScore(entry: LeaderboardEntry): number {
  if (entry.reviews <= 0) return 0;
  return Math.round((entry.total / entry.reviews) * 10) / 10;
}

/** Net lines: negative means the author removed more than they added. */
export function netLines(entry: LeaderboardEntry): number {
  return entry.additions - entry.deletions;
}

// The columns, in the order the board shows them. `sort` is what the daemon is
// asked to rank by; `value` is what the cell reads; `bar` says whether a
// magnitude bar is meaningful for it.
//
// Net lines has no bar on purpose. Removing code is the good outcome, so its
// board is ranked with the most negative first, and a bar scaled on magnitude
// would draw the biggest ADDER exactly like the biggest remover. A column
// where the best value is the smallest is a column a bar cannot describe.
export const columns: {
  sort: LeaderSort;
  label: string;
  value: (e: LeaderboardEntry) => number;
  bar: boolean;
  /** True for measures that describe a typical review rather than a total. */
  perReview?: boolean;
}[] = [
  { sort: 'total', label: 'Total', value: (e) => e.total, bar: true },
  { sort: 'reviews', label: 'Reviews', value: (e) => e.reviews, bar: true },
  { sort: 'approved', label: 'Approved', value: approvalRate, bar: true },
  { sort: 'mean', label: 'Mean', value: meanScore, bar: true, perReview: true },
  { sort: 'median', label: 'Median', value: (e) => e.median, bar: true, perReview: true },
  { sort: 'net', label: 'Net lines', value: netLines, bar: false },
];

export function columnFor(sort: LeaderSort) {
  return columns.find((c) => c.sort === sort) ?? columns[0];
}

// Below this many scored reviews, a per-review measure is one or two PRs
// wearing a trend's clothes: a single lucky change is a mean of itself. The
// row still ranks, because dropping somebody off a board they are on is worse
// than showing a number with a caveat, and the count is right there in its own
// column. It is drawn muted instead, the way this dashboard already dims a
// value that is inherited rather than set.
export const thinSample = 3;

export function isThinSample(entry: LeaderboardEntry, sort: LeaderSort): boolean {
  return Boolean(columnFor(sort).perReview) && entry.reviews < thinSample;
}


/** The display name if the roster knows one, else the handle. */
export function displayName(entry: LeaderboardEntry): string {
  return entry.name || entry.author;
}

/**
 * Why the board is empty, or '' when it is not.
 *
 * An empty board has three quite different causes and they must not look alike:
 * scoring switched off, a window with no reviews in it, and no reviews at all.
 */
export function emptyReason(enabled: boolean, entries: LeaderboardEntry[], days: number): string {
  if (!enabled) return 'Scoring is switched off in config (scoring.mode = disabled).';
  if (entries.length > 0) return '';
  if (days > 0) return `No reviews scored in the last ${days} days.`;
  return 'No reviews scored yet. Scores are recorded as reviews complete.';
}

/**
 * A banner for a board that is still shown but no longer growing.
 *
 * leaderboard-only stops the per-review GitHub call that measures a diff, so
 * the standings freeze where they are. Saying nothing would leave a board that
 * silently stops updating, which reads as a bug rather than a setting.
 */
export function frozenNotice(mode: ScoringMode | undefined): string {
  if (mode !== 'leaderboard-only') return '';
  return 'Scoring is paused (scoring.mode = leaderboard-only). These standings are final until it is turned back on.';
}



