// The policy-tuning panel's pure parts: the colour ramp its maps are painted
// with, and the round trip between an editable policy and the config document
// an operator would paste.
//
// No scoring arithmetic. Every number on that panel comes back from
// /api/score/simulate, computed by the daemon's own scorer, for the same
// reason the calculator asks rather than works it out: a tuning tool that
// disagrees with the scorer is worse than none, because it is the policy
// somebody then ships.

import type { ConfigResponse, ScoreBucket, ScoreCurveMode } from './types';

// Policy is the editable shape of the scoring document: the dials the panel
// offers, flat, with no optionality to reason about.
export type Policy = {
  base: number;
  churn_unit: number;
  churn_exponent: number;
  deletion_weight: number;
  shrink_bonus: number;
  attempt_decay: number;
  curve: ScoreCurveMode;
  buckets: ScoreBucket[];
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

// policyOf lifts the daemon's live policy into the editable shape, so the
// panel opens on what is actually running rather than on a blank form.
export function policyOf(c: ConfigResponse): Policy {
  const s = c.scoring;
  return {
    base: s.base,
    churn_unit: s.churn_unit,
    churn_exponent: s.churn_exponent,
    deletion_weight: s.deletion_weight,
    shrink_bonus: s.shrink_bonus,
    attempt_decay: s.attempt_decay,
    curve: s.curve,
    buckets: s.buckets.map((b) => ({ ...b })),
  };
}

// policyDoc is the scoring block as it goes over the wire, and as it would be
// written to config.json. The open-ended tier omits max_churn rather than
// sending a 0, which is what the config format means by open-ended.
export function policyDoc(p: Policy) {
  return {
    base: p.base,
    churn_unit: p.churn_unit,
    churn_exponent: p.churn_exponent,
    deletion_weight: p.deletion_weight,
    curve: p.curve,
    buckets: p.buckets.map((b) =>
      b.max_churn > 0
        ? { name: b.name, max_churn: b.max_churn, multiplier: b.multiplier }
        : { name: b.name, multiplier: b.multiplier },
    ),
    shrink_bonus: p.shrink_bonus,
    attempt_decay: p.attempt_decay,
  };
}

// policyJSON is what an operator pastes into config.json: the block, nested
// under the key it lives at, so it can be dropped in whole.
export function policyJSON(p: Policy): string {
  return JSON.stringify({ scoring: policyDoc(p) }, null, 2);
}

// sortTiers puts the ladder back in the only order it is allowed to be in.
//
// Where a tier sits is not information the operator supplies twice: validation
// requires strictly ascending max_churn, so the bound decides the position.
// Reordering by hand would be a second way to say the same thing, and a way to
// say it wrongly.
//
// The last row is pinned rather than sorted with the rest: it is the
// open-ended tier, it has no bound to sort by, and it has to stay last. A
// bounded row that gets its bound cleared therefore stays where it is and the
// daemon reports the problem, which is better than silently shuffling two
// catch-alls into a ladder that cannot resolve.
export function sortTiers(buckets: ScoreBucket[]): ScoreBucket[] {
  if (buckets.length < 3) return buckets;
  const last = buckets[buckets.length - 1];
  const bounded = buckets.slice(0, -1);
  const sorted = [...bounded].sort((a, b) => {
    if (a.max_churn > 0 && b.max_churn > 0) return a.max_churn - b.max_churn;
    return 0; // an unbounded row mid-ladder is an error to report, not to move
  });
  return [...sorted, last];
}

// changedFrom names the dials this policy moves, so the panel can say whether
// it is showing the daemon's policy or a draft. Buckets count as one.
export function changedFrom(draft: Policy, live: Policy): string[] {
  const keys: (keyof Policy)[] = [
    'base', 'churn_unit', 'churn_exponent', 'deletion_weight', 'shrink_bonus', 'attempt_decay', 'curve',
  ];
  const out = keys.filter((k) => draft[k] !== live[k]).map(String);
  if (JSON.stringify(draft.buckets) !== JSON.stringify(live.buckets)) out.push('buckets');
  return out;
}
