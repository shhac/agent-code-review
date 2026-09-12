import { describe, expect, it } from 'vitest';
import { areaPoints, linePoints, plotX, plotY, rateLines, scoreCurve, tierRows, type Plot, type ScoreCurve } from './scorecurve';
import type { ScoreAnchor, ScoreBucket } from './types';

// The shipped ladder and the anchors internal/score derives from it, as they
// arrive on the config response. The tail anchor (4000) is the daemon's
// number, not this file's: what it should be is pinned in Go, by
// TestAnchorsCarryTheLadderSpacingIntoTheTail.
const buckets: ScoreBucket[] = [
  { name: 'tiny', max_churn: 10, multiplier: 1 },
  { name: 'small', max_churn: 50, multiplier: 1.5 },
  { name: 'medium', max_churn: 250, multiplier: 1 },
  { name: 'large', max_churn: 1000, multiplier: 0.5 },
  { name: 'huge', max_churn: 0, multiplier: 0.2 },
];
const anchors: ScoreAnchor[] = [
  { churn: 10, multiplier: 1 },
  { churn: 50, multiplier: 1.5 },
  { churn: 250, multiplier: 1 },
  { churn: 1000, multiplier: 0.5 },
  { churn: 4000, multiplier: 0.2 },
];

const plot: Plot = { width: 720, height: 230, pad: { l: 46, r: 18, t: 22, b: 32 } };

// rateAt reads the DRAWN line, so these tests check what is on screen rather
// than the input they were handed.
function rateAt(c: ScoreCurve, churn: number): number {
  const points = c.points;
  for (let i = 1; i < points.length; i++) {
    const a = points[i - 1];
    const b = points[i];
    if (churn > b.churn) continue;
    if (b.churn === a.churn) return b.multiplier;
    const t = (Math.log(churn) - Math.log(a.churn)) / (Math.log(b.churn) - Math.log(a.churn));
    return a.multiplier + t * (b.multiplier - a.multiplier);
  }
  return points[points.length - 1].multiplier;
}

describe('the drawn line is the rate that is paid', () => {
  // The premise of drawing straight segments: score interpolates on
  // log(churn) and the x axis is log(churn), so a straight line between two
  // anchors IS the interpolation, with no sampling. 45 churn scores 132 rather
  // than 135 precisely because the rate there is 1.467x, and a chart drawing
  // 1.5x across the tier would be quietly lying about every PR that is not
  // sitting on a boundary, which is most of them.
  it('reproduces log-interpolation between the anchors', () => {
    const c = scoreCurve(buckets, anchors, 'linear');
    expect(rateAt(c, 45)).toBeCloseTo(1.4673, 3);
    expect(rateAt(c, 50)).toBeCloseTo(1.5, 6);
    expect(rateAt(c, 250)).toBeCloseTo(1, 6);
  });

  it('is flat below the first anchor and past the last', () => {
    const c = scoreCurve(buckets, anchors, 'linear');
    expect(rateAt(c, 1)).toBeCloseTo(1, 6);
    expect(rateAt(c, 10)).toBeCloseTo(1, 6);
    expect(rateAt(c, c.maxChurn)).toBeCloseTo(0.2, 6);
  });

  it('draws the cliffs as cliffs under step', () => {
    const c = scoreCurve(buckets, anchors, 'step');
    expect(rateAt(c, 1000)).toBeCloseTo(0.5, 6);
    expect(rateAt(c, 1001)).toBeCloseTo(0.2, 6);
    // Two vertices per tier is what puts a vertical riser at each boundary.
    expect(c.points.filter((p) => p.churn === 1000)).toHaveLength(2);
  });
});

describe('the bands name the tiers', () => {
  it('runs each tier from the previous boundary to its own', () => {
    const { bands } = scoreCurve(buckets, anchors, 'linear');
    expect(bands.map((b) => [b.name, b.from, b.to])).toEqual([
      ['tiny', 1, 10],
      ['small', 10, 50],
      ['medium', 50, 250],
      ['large', 250, 1000],
      ['huge', 1000, 4000],
    ]);
  });

  it('gives one gridline per distinct multiplier, low to high', () => {
    expect(rateLines(buckets)).toEqual([0.2, 0.5, 1, 1.5]);
  });
});

describe('coordinates stay inside the plot', () => {
  const c = scoreCurve(buckets, anchors, 'linear');

  it('spans the axis between the padding', () => {
    expect(plotX(c, plot, c.minChurn)).toBeCloseTo(plot.pad.l, 6);
    expect(plotX(c, plot, c.maxChurn)).toBeCloseTo(plot.width - plot.pad.r, 6);
    expect(plotY(c, plot, 0)).toBeCloseTo(plot.height - plot.pad.b, 6);
    expect(plotY(c, plot, c.maxMultiplier)).toBeCloseTo(plot.pad.t, 6);
  });

  // A churn off either end must not put a coordinate outside the box: the SVG
  // sets overflow visible, so it would be drawn over the page rather than
  // clipped away.
  it('clamps a churn beyond the drawn range', () => {
    expect(plotX(c, plot, 0)).toBeCloseTo(plot.pad.l, 6);
    expect(plotX(c, plot, 1e9)).toBeCloseTo(plot.width - plot.pad.r, 6);
  });

  it('closes the area along the baseline', () => {
    const base = (plot.height - plot.pad.b).toFixed(1);
    const area = areaPoints(c, plot).split(' ');
    expect(area[0]).toBe(`${plot.pad.l.toFixed(1)},${base}`);
    expect(area[area.length - 1]).toBe(`${(plot.width - plot.pad.r).toFixed(1)},${base}`);
    expect(area.slice(1, -1).join(' ')).toBe(linePoints(c, plot));
  });

  it('emits no NaN, whatever the ladder', () => {
    for (const line of [linePoints(c, plot), areaPoints(c, plot)]) {
      expect(line).not.toContain('NaN');
    }
  });
});

describe('a degenerate ladder still draws', () => {
  // One open-ended tier anchors at the left edge, which would otherwise leave
  // the x axis zero-wide and every coordinate NaN.
  it('gives a single flat tier a decade of axis', () => {
    const flat: ScoreBucket[] = [{ name: 'flat', max_churn: 0, multiplier: 0.8 }];
    const c = scoreCurve(flat, [{ churn: 1, multiplier: 0.8 }], 'linear');
    expect(c.maxChurn).toBeGreaterThan(c.minChurn);
    expect(rateAt(c, 5)).toBeCloseTo(0.8, 6);
    expect(linePoints(c, plot)).not.toContain('NaN');
  });

  it('returns an empty curve rather than NaN for nothing to draw', () => {
    expect(scoreCurve([], [], 'linear').points).toEqual([]);
    expect(scoreCurve(buckets, [], 'linear').points).toEqual([]);
    expect(scoreCurve([{ name: 'free', max_churn: 0, multiplier: 0 }], [{ churn: 1, multiplier: 0 }], 'linear').points).toEqual([]);
    expect(areaPoints(scoreCurve([], [], 'linear'), plot)).toBe('');
  });

  // An unrecognised curve draws the shipped one, matching how internal/score
  // scores it: the exception is named, never the default.
  it('draws anything that is not step as the ramp', () => {
    const c = scoreCurve(buckets, anchors, 'wobbly' as never);
    expect(rateAt(c, 45)).toBeCloseTo(1.4673, 3);
  });
});

describe('the ladder reads as a table', () => {
  it('states each tier as a range and a rate', () => {
    expect(tierRows(buckets)).toEqual([
      { name: 'tiny', range: 'up to 10', rate: '1x' },
      { name: 'small', range: '10 to 50', rate: '1.5x' },
      { name: 'medium', range: '50 to 250', rate: '1x' },
      { name: 'large', range: '250 to 1000', rate: '0.5x' },
      // The open-ended tier has no upper figure to quote, and the derived
      // anchor that ends the CHART is not a boundary anybody can fall off.
      { name: 'huge', range: '1000+', rate: '0.2x' },
    ]);
  });

  it('has nothing to state when there are no tiers', () => {
    expect(tierRows([])).toEqual([]);
  });

  // The table is the part a reader can act on, so it survives a ladder the
  // chart cannot draw: every tier pays a flat zero here, which has no shape.
  it('states tiers the chart has to give up on', () => {
    const flat: ScoreBucket[] = [
      { name: 'small', max_churn: 50, multiplier: 0 },
      { name: 'rest', max_churn: 0, multiplier: 0 },
    ];
    expect(scoreCurve(flat, [{ churn: 50, multiplier: 0 }], 'linear').points).toEqual([]);
    expect(tierRows(flat)).toEqual([
      { name: 'small', range: 'up to 50', rate: '0x' },
      { name: 'rest', range: '50+', rate: '0x' },
    ]);
  });

  it('says what a single open-ended tier covers', () => {
    expect(tierRows([{ name: 'flat', max_churn: 0, multiplier: 0.8 }])).toEqual([
      { name: 'flat', range: 'any size', rate: '0.8x' },
    ]);
  });
});

describe('a ladder the defaults did not anticipate still draws inside the box', () => {
  const plotted = (c: ReturnType<typeof scoreCurve>) =>
    c.points.map((p) => ({ x: plotX(c, plot, p.churn), y: plotY(c, plot, p.multiplier) }));

  // A tier ending below one line of churn is legal (deletions weigh 0.5, so
  // fractional churn is real). The axis used to start at a hardcoded 1, which
  // swallowed that tier's whole band.
  it('makes room for a tier narrower than one line', () => {
    const fine: ScoreBucket[] = [
      { name: 'trivial', max_churn: 0.5, multiplier: 2 },
      { name: 'rest', max_churn: 0, multiplier: 1 },
    ];
    const c = scoreCurve(fine, [{ churn: 0.5, multiplier: 2 }, { churn: 1, multiplier: 1 }], 'linear');
    expect(c.bands.map((b) => b.name)).toEqual(['trivial', 'rest']);
    expect(c.minChurn).toBeLessThan(0.5);
  });

  // A negative rate is legal too ("past here you lose points"). Plotted
  // against a domain that started at 0 it landed BELOW the box, and the svg
  // sets overflow visible, so it drew across the page instead of clipping.
  it('keeps a negative rate inside the plot', () => {
    const punitive: ScoreBucket[] = [
      { name: 'ok', max_churn: 100, multiplier: 1 },
      { name: 'too big', max_churn: 0, multiplier: -0.5 },
    ];
    const c = scoreCurve(punitive, [{ churn: 100, multiplier: 1 }, { churn: 200, multiplier: -0.5 }], 'linear');
    const bottom = plot.height - plot.pad.b;
    for (const { x, y } of plotted(c)) {
      expect(x).toBeGreaterThanOrEqual(plot.pad.l - 0.001);
      expect(x).toBeLessThanOrEqual(plot.width - plot.pad.r + 0.001);
      expect(y).toBeGreaterThanOrEqual(plot.pad.t - 0.001);
      expect(y).toBeLessThanOrEqual(bottom + 0.001);
    }
    // The zero line is inside the plot rather than at its floor, which is what
    // makes the dip below it readable.
    expect(plotY(c, plot, 0)).toBeLessThan(bottom);
  });
});
