import type { Candidate, Review, ReviewLogRef } from './types';

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
