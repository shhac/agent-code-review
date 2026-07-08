// Pure formatting helpers shared by every route.

import type { Candidate, UsageWindow } from './types';

const pad = (n: number) => String(n).padStart(2, '0');

// Absolute local timestamp in the house style: "YYYY-MM-DD @ HH:MM:SS".
// Accepts anything Date can parse (ISO strings, epoch millis, Dates).
export function when(t: string | number | Date) {
  if (!t) return '';
  const d = new Date(t);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} @ ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
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

// Relative time until a FUTURE timestamp ("42m", "1h42m", "2d3h"); "" once
// it has passed (or was never set) — the countdown shown on held queue rows.
// Compound units past the hour: "1.7h" reads like a measurement, "1h42m"
// like a countdown.
export function untilRel(t: string | undefined) {
  if (!t || new Date(t).getFullYear() < 2000) return '';
  const s = (new Date(t).getTime() - Date.now()) / 1000;
  if (s <= 0) return '';
  if (s < 60) return `${Math.ceil(s)}s`;
  const mins = Math.ceil(s / 60);
  if (mins < 60) return `${mins}m`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h${mins % 60 ? `${mins % 60}m` : ''}`;
  const days = Math.floor(hours / 24);
  return `${days}d${hours % 24 ? `${hours % 24}h` : ''}`;
}

// Human duration from a seconds count ("42s", "8m", "1.5h"). Zero and
// negative render as "" — history rows backfilled before duration tracking
// carry 0, which means unknown, not instant.
export function durSecs(s: number) {
  if (!s || s <= 0) return '';
  if (s < 90) return `${Math.round(s)}s`;
  if (s < 5400) return `${Math.round(s / 60)}m`;
  return `${(s / 3600).toFixed(1)}h`;
}

// Elapsed time between two known timestamps; here a zero gap is a real
// measurement, so it renders as "0s" rather than durSecs's unknown "".
export function dur(a: string, b: string) {
  if (!a || !b) return '';
  return durSecs(Math.max(0, (new Date(b).getTime() - new Date(a).getTime()) / 1000)) || '0s';
}

// Compact token count ("850", "3.4k", "193k"). Zero means unknown, not free.
export function tokens(n: number | undefined) {
  if (!n || n <= 0) return '';
  if (n < 1000) return `${n}`;
  if (n < 10000) return `${(n / 1000).toFixed(1)}k`;
  return `${Math.round(n / 1000)}k`;
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
  held: 'warn',
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
