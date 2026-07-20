import type { MetricsResponse } from './types';

export type ColourMode = 'verdict' | 'model';

type ScatterPoint = MetricsResponse['scatter'][number];
type ActivityDay = MetricsResponse['activity'][number];

// The primary review verdicts, in ring/legend order. Other values a review can
// carry (SKIPPED, ERROR) are deliberately not "primary": they fall through to
// the neutral scatter colour and the ring's trailing wedge. One source of truth
// for which verdicts get their own colour; the mappings below must stay in step.
export const VERDICTS = ['APPROVED', 'COMMENTED', 'REQUESTED_CHANGES'] as const;
export type Verdict = (typeof VERDICTS)[number];

const VERDICT_SET = new Set<string>(VERDICTS);
const isVerdict = (v: string): v is Verdict => VERDICT_SET.has(v);

// Scatter colour class per primary verdict; anything else renders as `other`.
const VERDICT_CLASS: Record<Verdict, string> = { APPROVED: 'approved', COMMENTED: 'commented', REQUESTED_CHANGES: 'rejected' };

// Ring wedge colour per primary verdict; the remainder fills with --amber.
const RING_COLOUR: Record<Verdict, string> = { APPROVED: 'var(--green)', COMMENTED: 'var(--blue)', REQUESTED_CHANGES: 'var(--red)' };

// Scatter dots inset from the axes so they never sit on the border lines:
// SCATTER_INSET% low-end margin, the remaining span carries the data range.
const SCATTER_INSET = 8;
const SCATTER_SPAN_X = 88;
const SCATTER_SPAN_Y = 82;

export function metricFacets(data: MetricsResponse | null) {
  const rows = data?.models || [];
  return {
    models: [...new Set(rows.map((m) => m.model).filter(Boolean))].sort(),
    efforts: [...new Set(rows.map((m) => m.effort).filter(Boolean))].sort(),
  };
}

// Palette slot per model present in the scatter: distinct names, sorted, take
// the four slots in turn. Ordinal, not hashed — a char-code hash mod 4 sent
// every real model name (and the empty Codex default) to the same slot, so
// "colour by model" painted the whole plot one colour.
export function modelSlots(scatter: ScatterPoint[]): Map<string, number> {
  const models = [...new Set(scatter.map((p) => p.model))].sort();
  return new Map(models.map((m, i) => [m, i % 4]));
}

export function scatterClass(point: ScatterPoint, mode: ColourMode, slots: Map<string, number>) {
  if (mode === 'model') return `model-${slots.get(point.model) ?? 0}`;
  return isVerdict(point.verdict) ? VERDICT_CLASS[point.verdict] : 'other';
}

// Absolute position (%) of one scatter dot within its plot, inset from the
// axes. Duration is conventionally read on the horizontal axis; token spend is
// on the vertical axis, so the caller passes duration then tokens as maxima.
export function scatterPos(point: ScatterPoint, maxDuration: number, maxTokens: number) {
	return {
		x: SCATTER_INSET + (point.duration_secs / maxDuration) * SCATTER_SPAN_X,
		y: SCATTER_INSET + (point.tokens_used / maxTokens) * SCATTER_SPAN_Y,
	};
}

// Round tick values for a 0..max axis, sized to land about three ticks, each
// positioned with the same inset+span maths as the dots so labels and
// gridlines sit exactly on the data scale. Token counts step 1/2/5 per decade;
// durations step along the clock ladder so labels read as round times.
const TIME_STEPS = [1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 14400, 28800, 43200, 86400];

function niceStep(rough: number): number {
  const magnitude = 10 ** Math.floor(Math.log10(rough));
  return [1, 2, 5, 10].map((m) => m * magnitude).find((s) => s >= rough) || 10 * magnitude;
}

function timeStep(rough: number): number {
  return TIME_STEPS.find((s) => s >= rough) || Math.ceil(rough / 86400) * 86400;
}

function axisTicks(max: number, span: number, step: number) {
  return Array.from({ length: Math.floor(max / step) }, (_, i) => {
    const value = step * (i + 1);
    return { value, pct: SCATTER_INSET + (value / max) * span };
  });
}

export const scatterTicksX = (max: number) => axisTicks(max, SCATTER_SPAN_X, timeStep(max / 4));
export const scatterTicksY = (max: number) => axisTicks(max, SCATTER_SPAN_Y, niceStep(max / 4));

// Inline style anchoring the hover tip to a dot's (x, y) percents: above and
// centred by default, flipped below near the top and nudged inward near the
// left/right edges so the tip never clips out of the plot.
export function scatterTipStyle(x: number, y: number): string {
  const below = y > 68;
  const tx = x > 70 ? '-85%' : x < 18 ? '-15%' : '-50%';
  const bottom = below ? `calc(${y}% - 14px)` : `calc(${y}% + 12px)`;
  return `left:${x}%; bottom:${bottom}; transform:translate(${tx}, ${below ? '100%' : '0'})`;
}

// conic-gradient stops for the verdict ring: one cumulative wedge per primary
// verdict (in VERDICTS order), then --amber for everything else. The denominator
// is the full verdict count, so the amber wedge is the non-primary remainder.
export function verdictRing(verdicts: Record<string, number>): string {
  const total = Math.max(1, Object.values(verdicts).reduce((a, b) => a + b, 0));
  let acc = 0;
  const stops = VERDICTS.map((v) => {
    acc += verdicts[v] || 0;
    return `${RING_COLOUR[v]} 0 ${(acc / total) * 360}deg`;
  });
  return `conic-gradient(${stops.join(', ')}, var(--amber) 0)`;
}

// SVG polyline points (0–100 viewBox) for the token-spend trend. A single day is
// centred at x=50 since it has no span to spread across; the y axis is inverted
// so higher spend sits nearer the top.
export function trendPoints(activity: ActivityDay[], maxTokens: number): string {
  return activity
    .map((day, i, rows) => {
      const x = rows.length < 2 ? 50 : (i / (rows.length - 1)) * 100;
      const y = 100 - (day.tokens_used / maxTokens) * 100;
      return `${x},${y}`;
    })
    .join(' ');
}
