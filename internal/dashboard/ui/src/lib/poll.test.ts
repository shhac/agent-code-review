import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// poll() registers component lifecycle hooks; run them inline so the chain
// can be driven without mounting a component, and capture the destroy
// callback to simulate unmount.
let destroyFn: (() => void) | undefined;
vi.mock('svelte', () => ({
  onMount: (fn: () => void) => fn(),
  onDestroy: (fn: () => void) => {
    destroyFn = fn;
  },
}));

import { poll } from './poll';

// The node test environment has no window; delegate to the (fake-timer
// patched) globals at call time.
vi.stubGlobal('window', {
  setTimeout: (fn: () => void, ms?: number) => setTimeout(fn, ms),
  clearTimeout: (id: ReturnType<typeof setTimeout>) => clearTimeout(id),
});

describe('poll', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    destroyFn = undefined;
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('runs immediately, then on the interval', async () => {
    const refresh = vi.fn(async () => {});
    poll(refresh, 5000);
    await vi.advanceTimersByTimeAsync(0);
    expect(refresh).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(5000);
    expect(refresh).toHaveBeenCalledTimes(2);
  });

  it('never overlaps a slow refresh: the next wait starts after completion', async () => {
    let release: () => void = () => {};
    const refresh = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          release = resolve;
        }),
    );
    poll(refresh, 1000);
    await vi.advanceTimersByTimeAsync(0);
    expect(refresh).toHaveBeenCalledTimes(1);
    // Refresh still pending: intervals elapsing must not start another.
    await vi.advanceTimersByTimeAsync(5000);
    expect(refresh).toHaveBeenCalledTimes(1);
    release();
    await vi.advanceTimersByTimeAsync(1000);
    expect(refresh).toHaveBeenCalledTimes(2);
    release();
  });

  it('reads a getter interval when scheduling each wait', async () => {
    const refresh = vi.fn(async () => {});
    let interval = 1000;
    poll(refresh, () => interval);
    await vi.advanceTimersByTimeAsync(0); // round 1; next wait already scheduled at 1000
    interval = 10000;
    await vi.advanceTimersByTimeAsync(1000); // round 2 fires on the old wait…
    expect(refresh).toHaveBeenCalledTimes(2);
    // …and the wait scheduled after it uses the getter's new cadence.
    await vi.advanceTimersByTimeAsync(9999);
    expect(refresh).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(refresh).toHaveBeenCalledTimes(3);
  });

  it('stops on destroy', async () => {
    const refresh = vi.fn(async () => {});
    poll(refresh, 1000);
    await vi.advanceTimersByTimeAsync(0);
    expect(destroyFn).toBeDefined();
    destroyFn?.();
    await vi.advanceTimersByTimeAsync(10000);
    expect(refresh).toHaveBeenCalledTimes(1);
  });
});
