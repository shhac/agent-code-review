import { describe, expect, it } from 'vitest';
import { moveByKey, reorderPayload } from './queueorder';
import type { Candidate } from './types';

const c = (number: number, status = 'queued') => ({ repo: 'o/r', number, status }) as Candidate;
const numbers = (list: Candidate[]) => list.map((x) => x.number);

describe('moveByKey', () => {
  const list = [c(1), c(2), c(3), c(4)];

  it('moves an item backward to land at the target position', () => {
    expect(numbers(moveByKey(list, 'o/r#3', 'o/r#1'))).toEqual([3, 1, 2, 4]);
  });

  it('moves an item forward to land at the target position', () => {
    expect(numbers(moveByKey(list, 'o/r#1', 'o/r#3'))).toEqual([2, 3, 1, 4]);
  });

  it('returns the list unchanged for unknown keys and no-op moves', () => {
    expect(moveByKey(list, 'o/r#9', 'o/r#1')).toBe(list);
    expect(moveByKey(list, 'o/r#1', 'o/r#9')).toBe(list);
    expect(moveByKey(list, 'o/r#2', 'o/r#2')).toBe(list);
  });

  it('does not mutate the input', () => {
    moveByKey(list, 'o/r#1', 'o/r#4');
    expect(numbers(list)).toEqual([1, 2, 3, 4]);
  });
});

describe('reorderPayload', () => {
  it('drops pinned reviewing rows and preserves display order', () => {
    const payload = reorderPayload([c(5, 'reviewing'), c(2), c(9), c(1)]);
    expect(payload).toEqual([
      { repo: 'o/r', number: 2 },
      { repo: 'o/r', number: 9 },
      { repo: 'o/r', number: 1 },
    ]);
  });

  it('is empty when only reviewing rows exist', () => {
    expect(reorderPayload([c(5, 'reviewing')])).toEqual([]);
  });
});
