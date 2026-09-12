import { describe, expect, it } from 'vitest';
import { changedFrom, heatColor, policyDoc, policyJSON, policyOf } from './scoreshape';
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
