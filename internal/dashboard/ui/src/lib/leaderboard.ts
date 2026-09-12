// Pure derivations for the Leaderboard page.
//
// The SCORE itself is never computed here: Go owns that (internal/score), so
// the CLI, the scheduler and this page cannot disagree about what a review was
// worth. What lives here is presentation — ranking ties, bar widths, the
// approval share — and it is separated from the component so it can be tested
// without mounting one.

import type { LeaderboardEntry, ScoringMode, Viewer } from './types';

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

/** A signed line count, with an explicit + so the sign is never ambiguous. */
export function signed(n: number): string {
  return n > 0 ? `+${n}` : String(n);
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

/** A review's points, signed so a penalty is never mistaken for a small gain. */
export function scoreLabel(score: number): string {
  return `${score > 0 ? '+' : ''}${score} pts`;
}

/**
 * The hover explanation for a score.
 *
 * The bucket is the one fact a reader cannot infer from the number: the same
 * diff scores very differently either side of a tier boundary, so a surprising
 * total is usually explained by which tier it landed in.
 */
export function scoreTitle(score: number, bucket: string): string {
  const base = `${score} points for the PR author`;
  return bucket ? `${base} (${bucket} diff)` : base;
}

/**
 * Whether these points belong to the person looking at the page.
 *
 * GitHub handles are case-insensitive, and the roster stores whatever case it
 * was given, so comparing them raw would fail to recognise somebody by the
 * capitalisation of their own name. An unidentified viewer owns nothing.
 */
export function isViewersScore(author: string, viewer: Viewer | null): boolean {
  if (!viewer?.handle || !author) return false;
  return author.toLowerCase() === viewer.handle.toLowerCase();
}
