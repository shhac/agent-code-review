import { describe, expect, it } from 'vitest';
import { errText } from './errors';

describe('errText', () => {
  it('uses an Error message', () => {
    expect(errText(new Error('that PR is not queued'))).toBe('that PR is not queued');
  });

  it('never returns an empty string', () => {
    // The bug this replaced: `catch (e: any) { msg = e.message }` yields
    // undefined for a non-Error rejection, and an empty error box reads as
    // "nothing happened" rather than "something failed".
    for (const thrown of [undefined, null, {}, new Error('')]) {
      expect(errText(thrown)).not.toBe('');
      expect(errText(thrown)).not.toContain('undefined');
      expect(errText(thrown)).not.toContain('object Object');
    }
  });

  it('passes a thrown string through', () => {
    expect(errText('rate limited')).toBe('rate limited');
  });
});
