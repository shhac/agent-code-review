import { describe, expect, it } from 'vitest';
import { steeringSession } from './steering';
import type { SteeringHold } from './types';

// A fake server plus a fake clock, so the rules are asserted without a browser
// or a real 60s wait. The interesting cases are all about what happens AFTER
// the server stops granting the hold, which is where the component version
// went wrong.
function harness(replies: SteeringHold[]) {
  const calls = { held: 0, freed: 0 };
  let fire: (() => void) | undefined;
  const session = steeringSession('o/r', 1, {
    hold: async () => {
      const r = replies[Math.min(calls.held, replies.length - 1)];
      calls.held += 1;
      if (r === undefined) throw new Error('boom');
      return r;
    },
    free: async () => {
      calls.freed += 1;
    },
    setTimer: (fn) => {
      fire = fn;
      return 1 as unknown as ReturnType<typeof setInterval>;
    },
    clearTimer: () => {
      fire = undefined;
    },
  });
  return { session, calls, tick: () => fire?.(), renewing: () => fire !== undefined };
}

describe('steeringSession', () => {
  it('parks the PR and keeps renewing while the server says held', async () => {
    const h = harness([{ state: 'held', until: 'x' }]);
    await h.session.start();
    expect(h.session.parked()).toBe(true);
    expect(h.renewing()).toBe(true);
  });

  it('stops renewing once capped, and stops claiming the PR is parked', async () => {
    const h = harness([{ state: 'held', until: 'x' }, { state: 'capped' }]);
    await h.session.start();
    h.tick();
    await Promise.resolve();
    await Promise.resolve();
    expect(h.session.parked()).toBe(false);
    expect(h.renewing()).toBe(false);
  });

  it('still releases after a cap', async () => {
    // The bug this module exists for. A capped session is no longer parked,
    // but the server still has a session marker for it; skipping the release
    // strands that marker and caps every later session on the row instantly.
    const h = harness([{ state: 'capped' }]);
    await h.session.start();
    expect(h.session.parked()).toBe(false);
    await h.session.release();
    expect(h.calls.freed).toBe(1);
  });

  it('still releases after a failed renewal', async () => {
    const h = harness([]);
    await h.session.start();
    await h.session.release();
    expect(h.calls.freed).toBe(1);
  });

  it('does not renew when holds are disabled', async () => {
    const h = harness([{ state: 'disabled' }]);
    await h.session.start();
    expect(h.renewing()).toBe(false);
    expect(h.session.parked()).toBe(false);
  });

  it('releases nothing when no session was ever started', async () => {
    const h = harness([]);
    await h.session.release();
    expect(h.calls.freed).toBe(0);
  });

  it('releases only once', async () => {
    const h = harness([{ state: 'held', until: 'x' }]);
    await h.session.start();
    await h.session.release();
    await h.session.release();
    expect(h.calls.freed).toBe(1);
  });
});
