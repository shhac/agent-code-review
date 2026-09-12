import { describe, expect, it } from 'vitest';
import { maxOf } from './format';
import { approvalRate, barWidth, columnFor, columns, displayName, emptyReason, frozenNotice, isThinSample, meanScore, medal, netLines, thinSample } from './leaderboard';
import type { LeaderboardEntry } from './types';

const entry = (over: Partial<LeaderboardEntry> = {}): LeaderboardEntry => ({
  rank: 1, author: 'alice', total: 100, median: 40, reviews: 2, approvals: 1, additions: 40, deletions: 10, ...over,
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

describe('netLines', () => {
  it('is negative when the author removed more than they added', () => {
    expect(netLines(entry({ additions: 10, deletions: 800 }))).toBe(-790);
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

describe('frozenNotice', () => {
  // A board that silently stops updating reads as a bug rather than a setting.
  it('explains a paused board', () => {
    expect(frozenNotice('leaderboard-only')).toContain('paused');
  });

  it('says nothing while scoring is running', () => {
    expect(frozenNotice('enabled')).toBe('');
  });

  // Disabled hides the page entirely, so emptyReason owns that message.
  it('says nothing when scoring is fully disabled', () => {
    expect(frozenNotice('disabled')).toBe('');
  });

  it('survives an unknown mode', () => {
    expect(frozenNotice(undefined)).toBe('');
  });
});

describe('the columns a board can rank by', () => {
  it('reads each cell off the entry', () => {
    const e = entry({ total: 300, median: 44, reviews: 10, approvals: 9, additions: 100, deletions: 900 });
    const read = (sort: Parameters<typeof columnFor>[0]) => columnFor(sort).value(e);
    expect(read('total')).toBe(300);
    expect(read('reviews')).toBe(10);
    expect(read('approved')).toBe(90);
    expect(read('mean')).toBe(30);
    expect(read('median')).toBe(44);
    expect(read('net')).toBe(-800);
  });

  // Removing code is the good outcome, so the net board is ranked with the
  // most negative first. A bar scaled on magnitude would draw the biggest
  // ADDER exactly like the biggest remover, which is why that column has none.
  it('offers no bar for the column whose best value is the smallest', () => {
    expect(columnFor('net').bar).toBe(false);
    expect(columns.filter((c) => !c.bar).map((c) => c.sort)).toEqual(['net']);
  });

  it('falls back to the total for a measure it does not know', () => {
    expect(columnFor('nonsense' as never).sort).toBe('total');
  });
});

describe('thin samples on a per-review board', () => {
  // A single lucky change is a mean of itself. The row still ranks, because
  // dropping somebody off a board they are on is worse than showing a number
  // with a caveat, but it is drawn muted.
  it('marks an author with too few reviews when the measure is per-review', () => {
    const thin = entry({ reviews: thinSample - 1 });
    expect(isThinSample(thin, 'mean')).toBe(true);
    expect(isThinSample(thin, 'median')).toBe(true);
  });

  it('says nothing about thin samples on a board ranked by volume', () => {
    const thin = entry({ reviews: 1 });
    for (const sort of ['total', 'reviews', 'approved', 'net'] as const) {
      expect(isThinSample(thin, sort)).toBe(false);
    }
  });

  it('leaves an author with enough reviews alone', () => {
    expect(isThinSample(entry({ reviews: thinSample }), 'median')).toBe(false);
  });
});
