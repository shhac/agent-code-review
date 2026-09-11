import { describe, expect, it } from 'vitest';
import { maxOf } from './format';
import { approvalRate, barWidth, displayName, emptyReason, meanScore, medal, netLines, signed } from './leaderboard';
import type { LeaderboardEntry } from './types';

const entry = (over: Partial<LeaderboardEntry> = {}): LeaderboardEntry => ({
  rank: 1, author: 'alice', total: 100, reviews: 2, approvals: 1, additions: 40, deletions: 10, ...over,
});

describe('medal', () => {
  it('marks only the top three', () => {
    expect(medal(1)).toBe('🥇');
    expect(medal(3)).toBe('🥉');
    expect(medal(4)).toBe('');
  });
});

describe('barWidth', () => {
  it('scales against the leader', () => {
    expect(barWidth(200, 200)).toBe(100);
    expect(barWidth(100, 200)).toBe(50);
  });

  // A negative total gets no bar rather than one pointing the wrong way.
  it('gives a negative total no bar', () => {
    expect(barWidth(-50, 100)).toBe(0);
  });

  it('survives a board where nobody is positive', () => {
    expect(barWidth(-10, 0)).toBe(0);
  });

  // maxOf's floor keeps the divisor safe on an empty board.
  it('survives an empty board', () => {
    expect(barWidth(0, maxOf([], (e: LeaderboardEntry) => e.total, 0))).toBe(0);
  });
});

describe('approvalRate', () => {
  it('is a percentage of scored reviews', () => {
    expect(approvalRate(entry({ reviews: 4, approvals: 3 }))).toBe(75);
  });

  it('is zero rather than NaN with no reviews', () => {
    expect(approvalRate(entry({ reviews: 0, approvals: 0 }))).toBe(0);
  });
});

describe('meanScore', () => {
  it('rounds to one decimal', () => {
    expect(meanScore(entry({ total: 100, reviews: 3 }))).toBe(33.3);
  });

  it('carries a negative mean', () => {
    expect(meanScore(entry({ total: -38, reviews: 1 }))).toBe(-38);
  });

  it('is zero rather than NaN with no reviews', () => {
    expect(meanScore(entry({ total: 0, reviews: 0 }))).toBe(0);
  });
});

describe('netLines and signed', () => {
  it('is negative when the author removed more than they added', () => {
    expect(netLines(entry({ additions: 10, deletions: 800 }))).toBe(-790);
    expect(signed(-790)).toBe('-790');
  });

  it('marks a positive net explicitly', () => {
    expect(signed(30)).toBe('+30');
    expect(signed(0)).toBe('0');
  });
});

describe('displayName', () => {
  it('prefers the roster name', () => {
    expect(displayName(entry({ name: 'Alice Example' }))).toBe('Alice Example');
  });

  it('falls back to the handle for an unrostered author', () => {
    expect(displayName(entry({ name: undefined }))).toBe('alice');
  });
});

describe('emptyReason', () => {
  // The three causes of an empty board must not look alike.
  it('distinguishes scoring being off', () => {
    expect(emptyReason(false, [], 0)).toContain('switched off');
  });

  it('distinguishes an empty window from an empty history', () => {
    expect(emptyReason(true, [], 30)).toContain('last 30 days');
    expect(emptyReason(true, [], 0)).toContain('No reviews scored yet');
  });

  it('says nothing when there is something to show', () => {
    expect(emptyReason(true, [entry()], 0)).toBe('');
  });
});
