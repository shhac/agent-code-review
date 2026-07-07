// Pure formatting helpers shared by every route.

import type { Candidate, UsageWindow } from './types';

export function when(t: string) {
  return t ? new Date(t).toLocaleString() : '';
}

// Relative time value ("5m", "3h"). Go zero times serialize as year 1;
// anything before 2000 is treated as "not set".
export function rel(t: string) {
  if (!t || new Date(t).getFullYear() < 2000) return '';
  const s = Math.max(0, (Date.now() - new Date(t).getTime()) / 1000);
  if (s < 60) return `${Math.floor(s)}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) return `${Math.floor(s / 3600)}h`;
  return `${Math.floor(s / 86400)}d`;
}

export function ago(t: string) {
  const r = rel(t);
  return r ? `${r} ago` : '';
}

// Elapsed time between two timestamps ("42s", "8m", "1.5h").
export function dur(a: string, b: string) {
  if (!a || !b) return '';
  const s = Math.max(0, (new Date(b).getTime() - new Date(a).getTime()) / 1000);
  if (s < 90) return `${Math.round(s)}s`;
  if (s < 5400) return `${Math.round(s / 60)}m`;
  return `${(s / 3600).toFixed(1)}h`;
}

export function keyOf(c: Candidate) {
  return `${c.repo}#${c.number}`;
}

export function prHref(repo: string, number: number, url?: string) {
  return url || `https://github.com/${repo}/pull/${number}`;
}

export function statusLabel(s: string) {
  return String(s ?? '').replace(/_/g, ' ');
}

// One vocabulary for candidate statuses, review verdicts, and run statuses.
// `live` states pulse their dot.
const kinds: Record<string, string> = {
  queued: 'dim',
  reviewing: 'info live',
  reviewed: 'ok',
  skipped: 'warn',
  error: 'bad',
  APPROVED: 'ok',
  COMMENTED: 'info',
  REQUESTED_CHANGES: 'bad',
  SKIPPED: 'warn',
  ERROR: 'bad',
  running: 'info live',
  done: 'ok',
  failed: 'bad',
  off: 'dim',
};

export function statusKind(s: string) {
  return kinds[s] || 'dim';
}

export function windowName(w: UsageWindow | undefined, fallback: string) {
  if (!w) return fallback;
  if (w.window_mins >= 10080) return 'Weekly';
  if (w.window_mins >= 60) return `${Math.round(w.window_mins / 60)}h window`;
  return `${w.window_mins}m window`;
}
