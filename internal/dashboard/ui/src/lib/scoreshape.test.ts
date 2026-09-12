import { describe, expect, it } from 'vitest';
import { changedFrom, curvePath, curveX, curveY, heatColor, policyDoc, policyJSON, policyOf, probeTrend, tierRanges, type Plot, type Policy } from './scoreshape';
import type { ConfigResponse } from './types';

const live: Policy = { piece_lines: 50, size_points: 100, size_falloff: 3, removal_points_per_100: 20 };
const config = { scoring: { ...live, approved: 2, commented: 0.1, requested_changes: -0.5, attempt_decay: 0.7 } } as ConfigResponse;
const plot: Plot = { width: 720, height: 240, pad: { l: 46, r: 18, t: 16, b: 30 } };

describe('a panel opens on what the daemon is running', () => {
  it('lifts the live policy out of the config response', () => {
    const cfg = { scoring: { ...live, mode: 'enabled' } } as unknown as ConfigResponse;
    expect(policyOf(cfg)).toEqual(live);
  });

  it('nests the block under the key it lives at, ready to paste', () => {
    const parsed = JSON.parse(policyJSON(live, config));
    expect(Object.keys(parsed)).toEqual(['scoring']);
    expect(parsed.scoring.piece_lines).toBe(50);
  });
});

describe('a draft says what it changed', () => {
  it('names the moved dials and nothing else', () => {
    expect(changedFrom(live, live)).toEqual([]);
    expect(changedFrom({ ...live, size_falloff: 4 }, live)).toEqual(['size_falloff']);
    expect(changedFrom({ ...live, piece_lines: 80, size_points: 50 }, live).sort()).toEqual(['piece_lines', 'size_points']);
  });
});

describe('the reward curve is drawn on a log axis', () => {
  // Linear, the peak this whole policy is built around is a spike two pixels
  // wide at the left of a twenty-thousand-line axis.
  const curve = [1, 10, 100, 1000, 10000].map((changed) => ({ changed, points: changed }));

  it('spans the plot between its padding', () => {
    expect(curveX(1, curve, plot)).toBeCloseTo(plot.pad.l, 6);
    expect(curveX(10000, curve, plot)).toBeCloseTo(plot.width - plot.pad.r, 6);
  });

  it('puts each decade the same distance apart', () => {
    const a = curveX(10, curve, plot) - curveX(1, curve, plot);
    const b = curveX(1000, curve, plot) - curveX(100, curve, plot);
    expect(a).toBeCloseTo(b, 6);
  });

  it('clamps a size outside the sampled range rather than drawing past the box', () => {
    expect(curveX(0, curve, plot)).toBeCloseTo(plot.pad.l, 6);
    expect(curveX(1e9, curve, plot)).toBeCloseTo(plot.width - plot.pad.r, 6);
  });

  it('puts zero points on the baseline and the peak at the top', () => {
    expect(curveY(0, plot, 100)).toBeCloseTo(plot.height - plot.pad.b, 6);
    expect(curveY(100, plot, 100)).toBeCloseTo(plot.pad.t, 6);
  });

  it('emits no NaN for an empty or flat curve', () => {
    expect(curvePath([], plot, 0)).toBe('');
    expect(curvePath(curve, plot, 0)).not.toContain('NaN');
  });
});

describe('the tier labels read as ranges', () => {
  it('states each label as the sizes it covers', () => {
    expect(tierRanges([
      { name: 'tiny', up_to: 12.5 },
      { name: 'small', up_to: 50 },
      { name: 'medium', up_to: 200 },
      { name: 'large', up_to: 1000 },
      { name: 'huge' },
    ])).toEqual([
      { name: 'tiny', range: 'up to 12' },
      { name: 'small', range: '13 to 50' },
      { name: 'medium', range: '51 to 200' },
      { name: 'large', range: '201 to 1000' },
      { name: 'huge', range: '1001+' },
    ]);
  });

  it('says what a single open-ended label covers', () => {
    expect(tierRanges([{ name: 'any' }])).toEqual([{ name: 'any', range: 'any size' }]);
  });
});

describe('the heat ramp', () => {
  it('runs dark to bright, and paints a negative score differently', () => {
    expect(heatColor(1)[1]).toBeGreaterThan(200);
    expect(heatColor(0)[2]).toBeLessThan(60);
    expect(heatColor(0.5, true)).toEqual(heatColor(1, true));
  });

  it('clamps rather than extrapolating', () => {
    expect(heatColor(-5)).toEqual(heatColor(0));
    expect(heatColor(9)).toEqual(heatColor(1));
    expect(heatColor(NaN)).toEqual(heatColor(0));
  });
});

describe('an untouched draft keeps configured outcome rewards', () => {
  it('uses the same full reward policy for simulation and export', () => {
    const policy = policyOf(config);
    const doc = policyDoc(policy, config);
    expect(doc).toMatchObject({ attempt_decay: 0.7, verdicts: { approved: 2, commented: 0.1, requested_changes: -0.5 } });
    expect(JSON.parse(policyJSON(policy, config)).scoring).toEqual(doc);
  });
});

describe('the comparison reports what the numbers say', () => {
  it.each([
    [[10, 9, 8], 'tighter wins'],
    [[8, 9, 10], 'bigger wins'],
    [[10, 10, 10], 'scores tie'],
    [[9, 10, 10], 'mixed scores'],
    [[9, 10, 9], 'mixed scores'],
  ])('labels %j as %s', (scores, label) => {
    expect(probeTrend(scores.map((score, lines) => ({ score, lines })))).toBe(label);
  });
});
