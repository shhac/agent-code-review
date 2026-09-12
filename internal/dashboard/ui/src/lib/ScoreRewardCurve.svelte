<script lang="ts">
  import { curvePath, curveX, curveY, type Plot } from './scoreshape';
  import type { ScoreSimulation } from './types';

  // What a PR earns for its size, against how many lines it changed. The
  // samples come from the daemon: the shape IS the policy, so a browser
  // plotting its own would be a second opinion about what a PR is worth.
  export let curve: ScoreSimulation['curve'] = [];
  export let peak = 0;
  export let pieceLines = 0;
  export let sizePoints = 0;

  const PLOT: Plot = { width: 720, height: 230, pad: { l: 46, r: 18, t: 18, b: 34 } };

  $: max = Math.max(sizePoints, ...curve.map((p) => p.points), 1);
  $: line = curvePath(curve, PLOT, max);
  $: area = line ? `${curveX(1, curve, PLOT).toFixed(1)},${curveY(0, PLOT, max).toFixed(1)} ${line} ${(PLOT.width - PLOT.pad.r).toFixed(1)},${curveY(0, PLOT, max).toFixed(1)}` : '';
  $: x = (changed: number) => curveX(changed, curve, PLOT);
  $: y = (points: number) => curveY(points, PLOT, max);
  // A decade grid, stopping at whatever the samples actually reach.
  $: decades = [1, 10, 100, 1000, 10000].filter((d) => curve.length && d <= curve[curve.length - 1].changed);
  // The two landmarks, which are the whole reason this chart exists.
  $: marks = [
    { at: pieceLines, label: 'best per line', points: 0 },
    { at: peak, label: 'best per PR', points: sizePoints },
  ].filter((m) => m.at > 0);
</script>

<figure class="reward-curve">
  <svg viewBox={`0 0 ${PLOT.width} ${PLOT.height}`} role="img" aria-label="Points earned for a PR's size, against how many lines it changed">
    {#each decades as d}
      <line class="grid" x1={x(d)} x2={x(d)} y1={PLOT.pad.t} y2={y(0)} />
      <text class="axis" x={x(d)} y={PLOT.height - PLOT.pad.b + 15} text-anchor="middle">{d}</text>
    {/each}
    <line class="grid dash" x1={PLOT.pad.l} x2={PLOT.width - PLOT.pad.r} y1={y(sizePoints)} y2={y(sizePoints)} />
    <text class="axis" x={PLOT.pad.l - 8} y={y(sizePoints) + 3.5} text-anchor="end">{Math.round(sizePoints)}</text>
    <line class="axis-line" x1={PLOT.pad.l} x2={PLOT.width - PLOT.pad.r} y1={y(0)} y2={y(0)} />
    <text class="axis" x={PLOT.pad.l - 8} y={y(0) + 3.5} text-anchor="end">0</text>

    {#if area}
      <polygon class="fill" points={area} />
      <polyline class="rate" points={line} />
    {/if}

    {#each marks as m}
      <line class="mark" x1={x(m.at)} x2={x(m.at)} y1={PLOT.pad.t} y2={y(0)} />
      <text class="mark-label" x={x(m.at)} y={PLOT.pad.t - 5} text-anchor="middle">{m.label} · {Math.round(m.at)}</text>
    {/each}
  </svg>
  <figcaption>
    Points for a PR's size, against the lines it changed (log scale). Below the
    left mark a PR is too small to be worth much, which is what stops work
    being chopped into fragments; past the right one it is worth less the
    bigger it gets.
  </figcaption>
</figure>

<style>
  .reward-curve { margin: 0; padding: 4px 20px 18px; }
  .reward-curve svg { width: 100%; height: auto; overflow: visible; }
  figcaption { margin-top: 8px; color: var(--dim); font-size: 12px; max-width: 68ch; }
  .grid { stroke: var(--line); stroke-width: 1; }
  .grid.dash { stroke-dasharray: 3 4; }
  .axis-line { stroke: var(--line-strong); stroke-width: 1; }
  .axis { fill: var(--faint); font-size: 11px; font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace; }
  .fill { fill: var(--accent); opacity: .12; }
  .rate { fill: none; stroke: var(--accent); stroke-width: 2.5; stroke-linejoin: round; }
  .mark { stroke: var(--ink); stroke-width: 1; stroke-dasharray: 2 3; opacity: .5; }
  .mark-label { fill: var(--dim); font-size: 10px; letter-spacing: .06em; }
</style>
