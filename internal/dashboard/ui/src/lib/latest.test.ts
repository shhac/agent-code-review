import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { latestWins } from './latest';

// Each request is held open until the test settles it, so answers can be made
// to land in any order.
function requests() {
  const open: { settle: (v: string) => void; fail: (e: unknown) => void }[] = [];
  const work = () =>
    new Promise<string>((resolve, reject) => {
      open.push({ settle: resolve, fail: reject });
    });
  return { open, work };
}

function recorder() {
  const shown: string[] = [];
  const errors: unknown[] = [];
  return { shown, errors, onValue: (v: string) => shown.push(v), onError: (e: unknown) => errors.push(e) };
}

describe('latestWins without a delay', () => {
  it('starts the request at once and applies its answer', async () => {
    const ask = latestWins();
    const r = requests();
    const out = recorder();
    ask(r.work, out.onValue, out.onError);
    expect(r.open).toHaveLength(1);
    r.open[0].settle('a');
    await vi.waitFor(() => expect(out.shown).toEqual(['a']));
  });

  it('drops a slower earlier answer that lands after the newer one', async () => {
    const ask = latestWins();
    const r = requests();
    const out = recorder();
    ask(r.work, out.onValue, out.onError);
    ask(r.work, out.onValue, out.onError);
    r.open[1].settle('second');
    await vi.waitFor(() => expect(out.shown).toEqual(['second']));
    r.open[0].settle('first');
    await Promise.resolve();
    await Promise.resolve();
    expect(out.shown).toEqual(['second']);
  });

  it('reports only the newest failure', async () => {
    const ask = latestWins();
    const r = requests();
    const out = recorder();
    ask(r.work, out.onValue, out.onError);
    ask(r.work, out.onValue, out.onError);
    r.open[0].fail(new Error('stale'));
    r.open[1].fail(new Error('current'));
    await vi.waitFor(() => expect(out.errors).toHaveLength(1));
    expect(out.errors).toEqual([new Error('current')]);
  });
});

describe('latestWins with a delay', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('asks once for a burst of changes', () => {
    const ask = latestWins(150);
    const r = requests();
    const out = recorder();
    ask(r.work, out.onValue, out.onError);
    vi.advanceTimersByTime(100);
    ask(r.work, out.onValue, out.onError);
    vi.advanceTimersByTime(100);
    expect(r.open).toHaveLength(0);
    vi.advanceTimersByTime(50);
    expect(r.open).toHaveLength(1);
  });

  // The bug both panels once had: the token was bumped when the request went
  // out, so an answer arriving inside the next change's debounce window still
  // counted as current and landed beside inputs it did not answer.
  it('discards an in-flight answer the moment the inputs change', async () => {
    const ask = latestWins(150);
    const r = requests();
    const out = recorder();
    ask(r.work, out.onValue, out.onError);
    vi.advanceTimersByTime(150);
    expect(r.open).toHaveLength(1);

    ask(r.work, out.onValue, out.onError);
    r.open[0].settle('answer to the old inputs');
    await vi.advanceTimersByTimeAsync(0);
    expect(out.shown).toEqual([]);

    await vi.advanceTimersByTimeAsync(150);
    r.open[1].settle('answer to the new inputs');
    await vi.advanceTimersByTimeAsync(0);
    expect(out.shown).toEqual(['answer to the new inputs']);
  });

  it('reads the inputs when the request goes out, not when it was asked', () => {
    const ask = latestWins(150);
    const out = recorder();
    const sent: number[] = [];
    let input = 1;
    ask(async () => {
      sent.push(input);
      return String(input);
    }, out.onValue, out.onError);
    input = 2;
    vi.advanceTimersByTime(150);
    expect(sent).toEqual([2]);
  });
});
