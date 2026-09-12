import { describe, expect, it } from 'vitest';
import { changedFrom, heatColor, ladderProblem, policyDoc, policyJSON, policyOf, sortTiers } from './scoreshape';
import type { ConfigResponse } from './types';
import type { Policy } from './scoreshape';

const live: Policy = {
  base: 100, churn_unit: 80, churn_exponent: 0.15, deletion_weight: 1.5,
  shrink_bonus: 1.6, attempt_decay: 0.4, curve: 'linear',
  buckets: [
    { name: 'small', max_churn: 50, multiplier: 1.5 },
    { name: 'huge', max_churn: 0, multiplier: 0.2 },
  ],
};

describe('the panel opens on what the daemon is running', () => {
  it('lifts the live policy out of the config response', () => {
    const cfg = { scoring: { ...live, mode: 'enabled' } } as unknown as ConfigResponse;
    expect(policyOf(cfg)).toEqual(live);
  });

  // Copied, not aliased: editing a dial must not quietly rewrite the config
  // the page is comparing against.
  it('copies the tiers rather than sharing them', () => {
    const cfg = { scoring: { ...live, mode: 'enabled' } } as unknown as ConfigResponse;
    const drafted = policyOf(cfg);
    drafted.buckets[0].multiplier = 9;
    expect(live.buckets[0].multiplier).toBe(1.5);
  });
});

describe('the document is the one config.json expects', () => {
  // An open-ended tier omits max_churn. Sending a 0 would be read as a tier
  // that covers nothing, which is not what the last row means.
  it('omits max_churn from the open-ended tier', () => {
    const doc = policyDoc(live);
    expect(doc.buckets[1]).toEqual({ name: 'huge', multiplier: 0.2 });
    expect(doc.buckets[0]).toEqual({ name: 'small', max_churn: 50, multiplier: 1.5 });
  });

  it('nests the block under the key it lives at, ready to paste', () => {
    const parsed = JSON.parse(policyJSON(live));
    expect(Object.keys(parsed)).toEqual(['scoring']);
    expect(parsed.scoring.churn_exponent).toBe(0.15);
  });
});

describe('a draft says what it changed', () => {
  it('names the moved dials and nothing else', () => {
    expect(changedFrom(live, live)).toEqual([]);
    expect(changedFrom({ ...live, churn_exponent: 0.4 }, live)).toEqual(['churn_exponent']);
    expect(changedFrom({ ...live, base: 50, curve: 'step' }, live)).toEqual(['base', 'curve']);
  });

  it('counts the whole ladder as one change', () => {
    const edited = { ...live, buckets: live.buckets.map((b) => ({ ...b, multiplier: 1 })) };
    expect(changedFrom(edited, live)).toEqual(['buckets']);
  });
});

describe('the heat ramp', () => {
  it('runs dark to bright, and paints a negative score differently', () => {
    const [, , bLow] = heatColor(0);
    const bright = heatColor(1);
    expect(bright[1]).toBeGreaterThan(200);
    expect(bLow).toBeLessThan(60);
    expect(heatColor(0.5, true)).toEqual(heatColor(1, true));
  });

  it('clamps rather than extrapolating', () => {
    expect(heatColor(-5)).toEqual(heatColor(0));
    expect(heatColor(9)).toEqual(heatColor(1));
    expect(heatColor(NaN)).toEqual(heatColor(0));
  });
});

describe('the ladder orders itself by the bounds it is given', () => {
  const ladder = (bounds: number[]) =>
    bounds.map((max_churn, i) => ({ name: `t${i}`, max_churn, multiplier: 1 }));

  // A tier added to the end with a bound of 120 belongs between 50 and 250.
  // Asking somebody to drag it there would be asking for the same fact twice.
  it('moves a new tier to where its bound puts it', () => {
    const got = sortTiers([...ladder([10, 50, 250]), { name: 'new', max_churn: 120, multiplier: 1 }, { name: 'open', max_churn: 0, multiplier: 0.2 }]);
    expect(got.map((b) => b.max_churn)).toEqual([10, 50, 120, 250, 0]);
  });

  // The open-ended tier has no bound to sort by and must stay last, or the
  // ladder stops resolving: it would match everything and strand the rest.
  it('pins the open-ended tier at the end', () => {
    const got = sortTiers([...ladder([250, 10, 50]), { name: 'open', max_churn: 0, multiplier: 0.2 }]);
    expect(got.map((b) => b.name).at(-1)).toBe('open');
    expect(got.map((b) => b.max_churn)).toEqual([10, 50, 250, 0]);
  });

  // A bound cleared mid-ladder is an error for the daemon to report, not
  // something to quietly shuffle into a second catch-all.
  it('leaves an unbounded middle row where the operator left it', () => {
    const got = sortTiers([...ladder([10, 0, 250]), { name: 'open', max_churn: 0, multiplier: 0.2 }]);
    expect(got.map((b) => b.max_churn)).toEqual([10, 0, 250, 0]);
  });

  it('has nothing to do with a two-row ladder', () => {
    const two = [...ladder([50]), { name: 'open', max_churn: 0, multiplier: 0.2 }];
    expect(sortTiers(two)).toBe(two);
  });
});

describe('a ladder the daemon will refuse says so next to the field', () => {
  const tier = (name: string, max_churn: number) => ({ name, max_churn, multiplier: 1 });

  it('is quiet about a ladder that resolves', () => {
    expect(ladderProblem([tier('small', 50), tier('large', 500), tier('huge', 0)])).toBe('');
  });

  // The mistake this exists for. "A first tier paying 0" sat under a column
  // headed Max churn, so the 0 went in the wrong field, and all the reader
  // got was the validator's own words about stranded buckets.
  it('names the tier left open in the middle of the ladder', () => {
    const got = ladderProblem([tier('tiny', 10), tier('medium', 0), tier('huge', 0)]);
    expect(got).toContain('Only the last tier can be open-ended');
    expect(got).toContain('medium');
  });

  it('catches two tiers claiming the same bound', () => {
    const got = ladderProblem([tier('tiny', 10), tier('small', 50), tier('medium', 50), tier('huge', 0)]);
    expect(got).toContain('share a max churn');
    expect(got).toContain('medium');
  });

  // Everything else is the daemon's to report: it validates the whole
  // ruleset, and a second opinion here could only drift from it.
  it('leaves the dials alone', () => {
    expect(ladderProblem([tier('only', 0)])).toBe('');
    expect(ladderProblem([])).toBe('');
  });
});
