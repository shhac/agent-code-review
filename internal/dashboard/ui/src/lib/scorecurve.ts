// The score ladder as GEOMETRY: where the rate line goes, and where the tier
// names and axis labels sit around it.
//
// Deliberately NOT a second opinion about what a tier is worth. The anchors
// (including the derived one for the open-ended tier, which is policy rather
// than drawing) are computed by internal/score and arrive on the config
// response; this file turns them into coordinates. An earlier draft ported
// that derivation into TypeScript and claimed the two were pinned together by
// matching test figures, which was wishful: numbers copied by hand stay green
// when the Go side changes, and a chart that disagrees with the daemon is
// worse than no chart, because it is believed.
//
// Straight segments between anchors are exact rather than sampled: score
// interpolates on log(churn) and the x axis IS log(churn), so between two
// anchors the line is straight.

import type { ScoreAnchor, ScoreBucket, ScoreCurveMode } from './types';

export type CurvePoint = { churn: number; multiplier: number };

// One tier as a band of the x axis: where its name belongs on the chart.
export type TierBand = { name: string; from: number; to: number; multiplier: number };

export type ScoreCurve = {
  points: CurvePoint[];
  bands: TierBand[];
  minChurn: number;
  maxChurn: number;
  // The y domain. It starts at 0 for any sane ladder and below it for one
  // that pays a negative rate, which validation permits (a policy of "past
  // here you lose points" is a choice an operator may make). Without the
  // lower bound those rates plotted below the box, and the SVG sets overflow
  // visible, so they were drawn across the page rather than clipped.
  minMultiplier: number;
  maxMultiplier: number;
};

// The plot box, in the units of the SVG's viewBox.
export type Plot = { width: number; height: number; pad: { l: number; r: number; t: number; b: number } };

// The default left edge: one line of churn, the smallest PR that scores
// anything at all (churn 0 scores nothing and is not on this curve). A ladder
// whose first tier ends below that pushes the edge down instead, or its first
// tier would have nowhere to be drawn.
const MIN_CHURN = 1;

const EMPTY: ScoreCurve = {
  points: [],
  bands: [],
  minChurn: MIN_CHURN,
  maxChurn: MIN_CHURN * 10,
  minMultiplier: 0,
  maxMultiplier: 1,
};

// scoreCurve is everything the chart needs from one ruleset.
export function scoreCurve(buckets: ScoreBucket[], anchors: ScoreAnchor[], curve: ScoreCurveMode): ScoreCurve {
  if (anchors.length === 0 || buckets.length === 0) return EMPTY;

  const minChurn = Math.min(MIN_CHURN, ...buckets.filter((b) => b.max_churn > 0).map((b) => b.max_churn / 2));
  // A degenerate ladder (one open-ended tier) anchors at the left edge, which
  // would otherwise leave the axis zero-wide and every coordinate NaN. Give it
  // a decade to be flat across.
  const maxChurn = Math.max(anchors[anchors.length - 1].churn, minChurn * 10);
  const rates = buckets.map((b) => b.multiplier);
  const minMultiplier = Math.min(0, ...rates);
  const maxMultiplier = Math.max(0, ...rates);
  // A ladder that pays nothing at every size has no shape to draw. The tier
  // table still states it, which is the part a reader can act on.
  if (maxMultiplier === minMultiplier) return EMPTY;

  const bands = bandsOf(buckets, minChurn, maxChurn);
  const points = curve === 'step' ? stepPoints(bands) : rampPoints(anchors, minChurn, maxChurn);
  return { points, bands, minChurn, maxChurn, minMultiplier, maxMultiplier };
}

// bandsOf lays the tiers along the axis. The open-ended tier runs to the right
// edge, which is the derived tail anchor: past it the rate is flat under
// either curve, so there is nothing further to show.
function bandsOf(buckets: ScoreBucket[], minChurn: number, maxChurn: number): TierBand[] {
  let from = minChurn;
  const bands: TierBand[] = [];
  for (const b of buckets) {
    const to = b.max_churn > 0 ? b.max_churn : maxChurn;
    if (to > from) bands.push({ name: b.name, from, to, multiplier: b.multiplier });
    from = to;
  }
  return bands;
}

// A staircase: two vertices per tier, so the risers are drawn as the cliffs
// they are.
function stepPoints(bands: TierBand[]): CurvePoint[] {
  return bands.flatMap((b) => [
    { churn: b.from, multiplier: b.multiplier },
    { churn: b.to, multiplier: b.multiplier },
  ]);
}

// The ramp is the anchors themselves, held flat at either end.
function rampPoints(anchors: ScoreAnchor[], minChurn: number, maxChurn: number): CurvePoint[] {
  const head = anchors[0].churn > minChurn ? [{ churn: minChurn, multiplier: anchors[0].multiplier }] : [];
  const last = anchors[anchors.length - 1];
  const tail = maxChurn > last.churn ? [{ churn: maxChurn, multiplier: last.multiplier }] : [];
  return [...head, ...anchors, ...tail];
}

// plotX places a churn on the log-scaled x axis, clamped to the drawn range so
// a figure off the end cannot put a coordinate outside the box.
export function plotX(c: ScoreCurve, plot: Plot, churn: number): number {
  const span = Math.log(c.maxChurn) - Math.log(c.minChurn);
  const at = Math.min(Math.max(churn, c.minChurn), c.maxChurn);
  const t = span > 0 ? (Math.log(at) - Math.log(c.minChurn)) / span : 0;
  return plot.pad.l + t * (plot.width - plot.pad.l - plot.pad.r);
}

// plotY places a multiplier on the linear y axis. The domain starts at 0 for
// any ladder that only pays, so the zero line IS the floor in the usual case.
export function plotY(c: ScoreCurve, plot: Plot, multiplier: number): number {
  const span = c.maxMultiplier - c.minMultiplier;
  const t = span > 0 ? (multiplier - c.minMultiplier) / span : 0;
  return plot.pad.t + (1 - t) * (plot.height - plot.pad.t - plot.pad.b);
}

// linePoints is the rate line as an SVG points attribute.
export function linePoints(c: ScoreCurve, plot: Plot): string {
  return c.points.map((p) => `${round1(plotX(c, plot, p.churn))},${round1(plotY(c, plot, p.multiplier))}`).join(' ');
}

// areaPoints is the same line closed along the baseline, so the area under the
// rate reads as the thing being earned rather than as an abstract plot.
export function areaPoints(c: ScoreCurve, plot: Plot): string {
  if (c.points.length === 0) return '';
  const base = round1(plotY(c, plot, 0));
  const first = round1(plotX(c, plot, c.points[0].churn));
  const last = round1(plotX(c, plot, c.points[c.points.length - 1].churn));
  return `${first},${base} ${linePoints(c, plot)} ${last},${base}`;
}

// TierRow is one line of the ladder table beside the chart: the tier, the
// churn it covers, and what that is worth.
export type TierRow = { name: string; range: string; rate: string };

// tierRows states the ladder in figures, because a chart cannot be read to two
// decimal places and the exact multiplier is what somebody checking their own
// PR came for.
//
// Built from the BUCKETS, not from the drawable bands: a tier the chart cannot
// show (one narrower than the axis can resolve, or a ladder with no shape at
// all) is still a tier that scores PRs, and dropping its row would hide the
// rule rather than the drawing.
export function tierRows(buckets: ScoreBucket[]): TierRow[] {
  let from = 0;
  return buckets.map((b, i) => {
    const last = i === buckets.length - 1;
    const range = last ? (from > 0 ? `${from}+` : 'any size') : from > 0 ? `${from} to ${b.max_churn}` : `up to ${b.max_churn}`;
    from = b.max_churn;
    return { name: b.name, range, rate: `${b.multiplier}x` };
  });
}

// rateLines are the horizontal gridlines: one per DISTINCT multiplier, because
// those are the numbers in the config and so the ones worth reading off.
export function rateLines(buckets: ScoreBucket[]): number[] {
  return [...new Set(buckets.map((b) => b.multiplier))].sort((a, b) => a - b);
}

function round1(n: number): string {
  return n.toFixed(1);
}
