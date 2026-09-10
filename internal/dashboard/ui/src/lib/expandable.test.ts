import { describe, expect, it } from 'vitest';
import { toggleIn } from './expandable';

describe('toggleIn', () => {
  it('adds a key that is absent and removes one that is present', () => {
    expect([...toggleIn(new Set<string>(), 'a')]).toEqual(['a']);
    expect([...toggleIn(new Set(['a']), 'a')]).toEqual([]);
  });

  it('leaves the original alone', () => {
    // The property the three hand-rolled versions disagreed about: a Set
    // passed to a child as a prop must not be mutated under it.
    const original = new Set(['a']);
    const next = toggleIn(original, 'b');
    expect([...original]).toEqual(['a']);
    expect([...next].sort()).toEqual(['a', 'b']);
  });

  it('touches only the key given', () => {
    expect([...toggleIn(new Set(['a', 'b']), 'a')]).toEqual(['b']);
  });
});
