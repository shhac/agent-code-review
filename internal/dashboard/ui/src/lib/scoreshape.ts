// The scoring pages' pure parts: the colour ramp their maps are painted with,
// the geometry of the reward curve, and the round trip between an editable
// policy and the config document an operator would paste.
//
// No scoring arithmetic. Every number on those pages comes back from
// /api/score/preview or /api/score/simulate, computed by the daemon's own
// scorer, for the same reason the anchors used to: a page that disagrees with
// the scorer is worse than no page, because its answer is the policy somebody
// then ships.

import type { ConfigResponse, ScoreSimulation } from './types';

// Policy is the editable shape of the scoring document: four dials, flat, with
// no optionality to reason about.
export type Policy = {
  piece_lines: number;
  size_points: number;
  size_falloff: number;
  removal_points_per_100: number;
};

// A single-hue luminance ramp in the dashboard's own green: brighter is worth
// more. Negative scores take the warning ink instead, because "less green" and
// "below zero" are different facts and must not look alike.
const RAMP: [number, [number, number, number]][] = [
  [0, [18, 21, 15]],
  [0.12, [26, 46, 32]],
  [0.35, [45, 89, 44]],
  [0.62, [104, 160, 44]],
  [0.85, [168, 217, 74]],
  [1, [232, 245, 192]],
];

const BAD: [number, number, number] = [96, 40, 36];

export function heatColor(t: number, negative = false): [number, number, number] {
  if (negative) return BAD;
  if (!(t > 0)) return RAMP[0][1];
  if (t >= 1) return RAMP[RAMP.length - 1][1];
  for (let i = 1; i < RAMP.length; i++) {
    if (t > RAMP[i][0]) continue;
    const [a0, a] = RAMP[i - 1];
    const [b0, b] = RAMP[i];
    const u = (t - a0) / (b0 - a0);
    return [
      Math.round(a[0] + u * (b[0] - a[0])),
      Math.round(a[1] + u * (b[1] - a[1])),
      Math.round(a[2] + u * (b[2] - a[2])),
    ];
  }
  return RAMP[RAMP.length - 1][1];
}

export function rampCSS(): string {
  return `linear-gradient(90deg,${RAMP.map(([at, c]) => `rgb(${c.join(',')}) ${Math.round(at * 100)}%`).join(',')})`;
}

// policyOf lifts the daemon's live policy into the editable shape, so a panel
// opens on what is actually running rather than on a blank form.
export function policyOf(c: ConfigResponse): Policy {
  const s = c.scoring;
  return {
    piece_lines: s.piece_lines,
    size_points: s.size_points,
    size_falloff: s.size_falloff,
    removal_points_per_100: s.removal_points_per_100,
  };
}

// The four visible dials do not contain the outcome policy. Preserve it in
// both the simulation and the export, or an untouched draft quietly reverts
// configured verdicts and decay to the shipped defaults.
export function policyDoc(p: Policy, c: ConfigResponse) {
  const s = c.scoring;
  return {
    ...p,
    attempt_decay: s.attempt_decay,
    verdicts: { approved: s.approved, commented: s.commented, requested_changes: s.requested_changes },
  };
}

export function policyJSON(p: Policy, c: ConfigResponse): string {
  return JSON.stringify({ scoring: policyDoc(p, c) }, null, 2);
}

export function probeTrend(probes: ScoreSimulation['probes']): string {
  const scores = probes.map((p) => p.score);
  if (scores.every((s) => s === scores[0])) return 'scores tie';
  if (scores.every((s, i) => i === 0 || s < scores[i - 1])) return 'tighter wins';
  if (scores.every((s, i) => i === 0 || s > scores[i - 1])) return 'bigger wins';
  return 'mixed scores';
}

// changedFrom names the dials this policy moves, so a panel can say whether it
// is showing the daemon's policy or a draft.
export function changedFrom(draft: Policy, live: Policy): string[] {
  return (Object.keys(draft) as (keyof Policy)[]).filter((k) => draft[k] !== live[k]);
}

// The plot box, in the units of an SVG viewBox.
export type Plot = { width: number; height: number; pad: { l: number; r: number; t: number; b: number } };

// curvePath turns the daemon's sampled reward curve into an SVG polyline.
//
// The x axis is logarithmic because the interesting part is all at the small
// end: on a linear axis out to twenty thousand lines, the peak this whole
// policy is built around is a spike two pixels wide.
export function curvePath(curve: ScoreSimulation['curve'], plot: Plot, maxPoints: number): string {
  return curve
    .filter((p) => p.changed >= 1)
    .map((p) => `${curveX(p.changed, curve, plot).toFixed(1)},${curveY(p.points, plot, maxPoints).toFixed(1)}`)
    .join(' ');
}

export function curveX(changed: number, curve: ScoreSimulation['curve'], plot: Plot): number {
  const hi = curve.length ? curve[curve.length - 1].changed : 1;
  const span = Math.log(Math.max(hi, 2));
  const at = Math.min(Math.max(changed, 1), hi);
  return plot.pad.l + (Math.log(at) / span) * (plot.width - plot.pad.l - plot.pad.r);
}

export function curveY(points: number, plot: Plot, maxPoints: number): number {
  const t = maxPoints > 0 ? points / maxPoints : 0;
  return plot.pad.t + (1 - t) * (plot.height - plot.pad.t - plot.pad.b);
}

// tierRanges states the labels as ranges, because a boundary on its own does
// not say which side a PR falls on. The last tier is open-ended.
export function tierRanges(tiers: { name: string; up_to?: number }[]): { name: string; range: string }[] {
  let from = 0;
  return tiers.map((t, i) => {
    const last = i === tiers.length - 1 || !t.up_to;
    const to = Math.floor(t.up_to ?? 0);
    const range = last ? (from > 0 ? `${from}+` : 'any size')
      : to < from ? 'no whole-line sizes'
      : from === to ? String(to)
      : from > 0 ? `${from} to ${to}` : `up to ${to}`;
    from = to + 1;
    return { name: t.name, range };
  });
}
