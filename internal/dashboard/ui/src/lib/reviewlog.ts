import { exact } from './format';
import type { Candidate, Review, ReviewLogPr, ReviewLogRef } from './types';

const reviewPath = /^\/review\/([^/]+\/[^/]+)\/(\d+)(?:\/([^/]+))?$/;

export function liveReviewLogRef(c: Candidate): ReviewLogRef {
  return { repo: c.repo, number: c.number };
}

export function reviewLogRefFromReview(r: Review): ReviewLogRef | null {
  if (!r.work_dir || !r.log_key) return null;
  return { repo: r.repo, number: r.number, logKey: r.log_key };
}

export function reviewLogPath(ref: ReviewLogRef) {
  return `/review/${ref.repo}/${ref.number}${ref.logKey ? `/${ref.logKey}` : ''}`;
}

export function reviewLogPathFromReview(r: Review) {
  const ref = reviewLogRefFromReview(r);
  return ref ? reviewLogPath(ref) : '';
}

export function parseReviewLogPath(path: string): ReviewLogRef | null {
  const m = reviewPath.exec(path);
  if (!m) return null;
  return { repo: m[1], number: Number(m[2]), logKey: m[3] || undefined };
}

export function reviewLogRouteKey(ref: ReviewLogRef) {
  return `${ref.repo}#${ref.number}#${ref.logKey || ''}`;
}

// tokenDetail explains a review's token total. The split behind it has three
// honest readings, not two: a review recorded before the split existed knows
// neither half, and saying "none re-read from cache" about a claude run would
// be a confident lie. No total at all has nothing to explain.
export function tokenDetail(pr: Pick<ReviewLogPr, 'tokens_used' | 'fresh_tokens' | 'cache_read_tokens'> | null): string {
  if (!pr?.tokens_used) return '';
  if (pr.cache_read_tokens) return `${exact(pr.fresh_tokens)} processed + ${exact(pr.cache_read_tokens)} re-read from cache`;
  if (pr.fresh_tokens) return `${exact(pr.fresh_tokens)} processed, none re-read from cache`;
  return 'recorded before this engine reported the split';
}
