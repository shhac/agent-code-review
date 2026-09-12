<script lang="ts">
  import { areaPoints, linePoints, plotX, plotY, rateLines, scoreCurve, type Plot } from './scorecurve';
  import type { ScoreAnchor, ScoreBucket, ScoreCurveMode } from './types';

  export let buckets: ScoreBucket[] = [];
  export let anchors: ScoreAnchor[] = [];
  export let curve: ScoreCurveMode = 'linear';

  // A fixed viewBox scaled by CSS: the chart is one shape, so it can keep its
  // proportions at any width instead of re-laying out per breakpoint. The
  // coordinate maths lives in lib/scorecurve.ts, where it is testable, as the
  // Metrics page already does with lib/metrics.ts.
  const PLOT: Plot = { width: 720, height: 230, pad: { l: 46, r: 18, t: 22, b: 32 } };

  $: c = scoreCurve(buckets, anchors, curve);
  $: x = (churn: number) => plotX(c, PLOT, churn);
  $: y = (multiplier: number) => plotY(c, PLOT, multiplier);
  $: line = linePoints(c, PLOT);
  $: area = areaPoints(c, PLOT);
  $: rates = rateLines(buckets);
  // Every tier end gets a number under the axis; only the inner ones get a
  // separator, because the right edge is where the chart stops rather than a
  // cliff anybody falls off. Past it the rate is flat under either curve,
  // which is what the trailing "+" says.
  $: lastBand = c.bands.length - 1;
</script>

<figure class="curve">
  <svg viewBox={`0 0 ${PLOT.width} ${PLOT.height}`} role="img" aria-label={`Size multiplier against lines of churn, as a ${curve} curve`}>
    {#each rates as m}
      <line class="grid" x1={PLOT.pad.l} x2={PLOT.width - PLOT.pad.r} y1={y(m)} y2={y(m)} />
      <text class="axis" x={PLOT.pad.l - 8} y={y(m) + 3.5} text-anchor="end">{m}x</text>
    {/each}
    <line class="axis-line" x1={PLOT.pad.l} x2={PLOT.width - PLOT.pad.r} y1={y(0)} y2={y(0)} />
    <text class="axis" x={PLOT.pad.l - 8} y={y(0) + 3.5} text-anchor="end">0x</text>

    {#each c.bands as b}
      <text class="band" x={(x(b.from) + x(b.to)) / 2} y={PLOT.pad.t - 8} text-anchor="middle">{b.name}</text>
    {/each}
    {#each c.bands as b, i}
      {#if i < lastBand}
        <line class="bound" x1={x(b.to)} x2={x(b.to)} y1={PLOT.pad.t - 4} y2={y(0)} />
      {/if}
      <text class="axis" x={x(b.to)} y={PLOT.height - PLOT.pad.b + 16} text-anchor={i === lastBand ? 'end' : 'middle'}>
        {i === lastBand ? `${b.to}+` : b.to}
      </text>
    {/each}

    <polygon class="fill" points={area} />
    <polyline class="rate" points={line} />
    {#if curve === 'linear'}
      {#each anchors as a}
        <circle class="anchor" cx={x(a.churn)} cy={y(a.multiplier)} r="3.5" />
      {/each}
    {/if}
  </svg>
  <figcaption>
    Rate multiplier against lines of churn (log scale).
    {#if curve === 'linear'}
      Dots are the tiers' anchors; between them the rate ramps, so no single line is worth much.
    {:else}
      Each tier is one flat rate, so every riser is a cliff a PR can fall off by one line.
    {/if}
  </figcaption>
</figure>

<style>
  .curve { margin: 0; padding: 4px 20px 18px; }
  .curve svg { width: 100%; height: auto; overflow: visible; }
  figcaption { margin-top: 8px; color: var(--dim); font-size: 12px; }
  .grid { stroke: var(--line); stroke-width: 1; stroke-dasharray: 3 4; }
  .axis-line { stroke: var(--line-strong); stroke-width: 1; }
  .bound { stroke: var(--line); stroke-width: 1; }
  .axis { fill: var(--faint); font-size: 11px; font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace; }
  .band { fill: var(--dim); font-size: 11px; letter-spacing: .08em; text-transform: uppercase; }
  .fill { fill: var(--accent); opacity: .12; }
  .rate { fill: none; stroke: var(--accent); stroke-width: 2.5; stroke-linejoin: round; }
  .anchor { fill: var(--accent); }
</style>
