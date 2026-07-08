import { describe, expect, it } from 'vitest';
import { parseReviewLogPath, reviewLogPath, reviewLogRefFromReview, reviewLogRouteKey } from './reviewlog';
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
