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

// Stable name→palette-slot hash: summing char codes keeps a given model on the
// same one of four scatter colours run to run. Collisions are fine; it's cosmetic.
export function modelColourIndex(model: string): number {
  return [...model].reduce((n, c) => n + c.charCodeAt(0), 0) % 4;
}

export function scatterClass(point: ScatterPoint, mode: ColourMode) {
  if (mode === 'model') return `model-${modelColourIndex(point.model)}`;
  return isVerdict(point.verdict) ? VERDICT_CLASS[point.verdict] : 'other';
}

// Absolute position of one scatter dot within its plot, inset from the axes.
// Duration is conventionally read on the horizontal axis; token spend is on
// the vertical axis, so the caller passes duration then tokens as maxima.
export function scatterPos(point: ScatterPoint, maxDuration: number, maxTokens: number) {
	return {
		left: `${SCATTER_INSET + (point.duration_secs / maxDuration) * SCATTER_SPAN_X}%`,
		bottom: `${SCATTER_INSET + (point.tokens_used / maxTokens) * SCATTER_SPAN_Y}%`,
	};
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
