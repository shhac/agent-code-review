import { describe, expect, it } from 'vitest';
import { isViewer } from './viewer';
import type { Viewer } from './types';

const v = (handle?: string): Viewer | null => (handle ? { state: 'author', handle } : null);

describe('isViewer', () => {
  it('recognises the viewer', () => {
    expect(isViewer('alice', v('alice'))).toBe(true);
  });

  // Handles are case-insensitive and the roster keeps whatever case it was
  // given, so a raw comparison would fail to recognise somebody by the
  // capitalisation of their own name.
  it('ignores case in both directions', () => {
    expect(isViewer('Alice', v('alice'))).toBe(true);
    expect(isViewer('alice', v('ALICE'))).toBe(true);
  });

  it('does not claim somebody else', () => {
    expect(isViewer('bob', v('alice'))).toBe(false);
  });

  // Without this an unidentified viewer would match every row whose author was
  // not recorded, and the whole page would light up as theirs.
  it('owns nothing when unidentified', () => {
    expect(isViewer('alice', null)).toBe(false);
    expect(isViewer('alice', { state: 'anonymous' })).toBe(false);
    expect(isViewer('', v('alice'))).toBe(false);
    expect(isViewer('', { state: 'unmapped', handle: '' })).toBe(false);
  });
});
