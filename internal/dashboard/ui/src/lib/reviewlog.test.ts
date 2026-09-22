import { describe, expect, it } from 'vitest';
import { parseReviewLogPath, reviewLogPath, reviewLogRefFromReview, reviewLogRouteKey, tokenDetail } from './reviewlog';
import type { Review } from './types';

describe('review log identity helpers', () => {
  it('round-trips live and exact review-log paths', () => {
    expect(parseReviewLogPath('/review/o/r/7')).toEqual({ repo: 'o/r', number: 7, logKey: undefined });
    const exact = { repo: 'o/r', number: 7, logKey: 'abc123' };
    expect(reviewLogPath(exact)).toBe('/review/o/r/7/abc123');
    expect(parseReviewLogPath(reviewLogPath(exact))).toEqual(exact);
    expect(reviewLogRouteKey(exact)).toBe('o/r#7#abc123');
  });

  it('only creates history refs for reviews with a stored log', () => {
    const base = { repo: 'o/r', number: 7, log_key: 'k', work_dir: '/tmp/wd' } as Review;
    expect(reviewLogRefFromReview(base)).toEqual({ repo: 'o/r', number: 7, logKey: 'k' });
    expect(reviewLogRefFromReview({ ...base, work_dir: '' })).toBeNull();
    expect(reviewLogRefFromReview({ ...base, log_key: '' })).toBeNull();
  });
});

describe('the token total explains itself', () => {
  it('has nothing to explain without a total', () => {
    expect(tokenDetail(null)).toBe('');
    expect(tokenDetail({ tokens_used: 0, fresh_tokens: 5, cache_read_tokens: 5 })).toBe('');
  });

  it('splits a total that re-read from cache', () => {
    expect(tokenDetail({ tokens_used: 1500000, fresh_tokens: 300000, cache_read_tokens: 1200000 })).toBe(
      '300,000 processed + 1,200,000 re-read from cache',
    );
  });

  it('says plainly when nothing was re-read', () => {
    expect(tokenDetail({ tokens_used: 4200, fresh_tokens: 4200, cache_read_tokens: 0 })).toBe(
      '4,200 processed, none re-read from cache',
    );
  });

  // A row from before the split knows neither half. Reading its zeroes as
  // "none re-read" would state a fact nobody measured.
  it('does not invent a split the engine never reported', () => {
    expect(tokenDetail({ tokens_used: 4200, fresh_tokens: 0, cache_read_tokens: 0 })).toBe(
      'recorded before this engine reported the split',
    );
  });
});
