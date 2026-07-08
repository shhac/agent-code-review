import { describe, expect, it } from 'vitest';
import { dur, durSecs, prHref, statusKind, statusLabel, tokens, when } from './format';

describe('when', () => {
  it('renders the house YYYY-MM-DD @ HH:MM:SS style in local time', () => {
    expect(when(new Date(2026, 6, 8, 15, 13, 53))).toBe('2026-07-08 @ 15:13:53');
    expect(when(new Date(2026, 0, 2, 3, 4, 5))).toBe('2026-01-02 @ 03:04:05');
  });

  it('treats empty input as unknown', () => {
    expect(when('')).toBe('');
  });
});

describe('durSecs', () => {
  it('renders the seconds/minutes/hours ladder', () => {
    expect(durSecs(42)).toBe('42s');
    expect(durSecs(480)).toBe('8m');
    expect(durSecs(5400)).toBe('1.5h');
  });

  it('renders zero as unknown — backfilled history rows carry 0', () => {
    expect(durSecs(0)).toBe('');
    expect(durSecs(-5)).toBe('');
  });
});

describe('dur', () => {
  it('renders a real zero gap as 0s, unlike durSecs', () => {
    expect(dur('2026-07-07T12:00:00Z', '2026-07-07T12:00:00Z')).toBe('0s');
    expect(dur('2026-07-07T12:00:00Z', '2026-07-07T12:08:00Z')).toBe('8m');
    expect(dur('', '2026-07-07T12:00:00Z')).toBe('');
  });
});

describe('tokens', () => {
  it('compacts counts and treats zero as unknown', () => {
    expect(tokens(0)).toBe('');
    expect(tokens(undefined)).toBe('');
    expect(tokens(850)).toBe('850');
    expect(tokens(3421)).toBe('3.4k');
    expect(tokens(192575)).toBe('193k');
  });
});

describe('status vocabulary', () => {
  it('maps known states and dims unknown ones', () => {
    expect(statusKind('reviewing')).toBe('info live');
    expect(statusKind('APPROVED')).toBe('ok');
    expect(statusKind('whatever')).toBe('dim');
    expect(statusLabel('REQUESTED_CHANGES')).toBe('REQUESTED CHANGES');
  });
});

describe('prHref', () => {
  it('prefers the recorded URL and falls back to the canonical one', () => {
    expect(prHref('o/r', 5, 'https://example.test/x')).toBe('https://example.test/x');
    expect(prHref('o/r', 5)).toBe('https://github.com/o/r/pull/5');
  });
});
