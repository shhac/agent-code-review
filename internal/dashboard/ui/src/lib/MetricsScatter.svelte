<script lang="ts">
  // The Metrics page's duration-against-tokens plot, one dot per completed
  // review, coloured by verdict or by model.
  import { durSecs, maxOf, modelLabel, statusLabel, tokens } from './format';
  import { modelSlots, scatterDots, scatterTicksX, scatterTicksY, scatterTipStyle, type ColourMode, type ScatterDot } from './metrics';
  import type { MetricsResponse } from './types';

  export let scatter: MetricsResponse['scatter'];

  let colour: ColourMode = 'verdict';
  let tip: { point: ScatterDot['point']; cls: string; style: string } | null = null;

  $: maxDuration = maxOf(scatter, (p) => p.duration_secs);
  $: maxTokens = maxOf(scatter, (p) => p.fresh_tokens);
  $: slots = modelSlots(scatter);
  $: xTicks = scatterTicksX(maxDuration);
  $: yTicks = scatterTicksY(maxTokens);
  $: dots = scatterDots(scatter, colour, slots, maxDuration, maxTokens);

  const show = (dot: ScatterDot) => (tip = { point: dot.point, cls: dot.cls, style: scatterTipStyle(dot.x, dot.y) });
</script>

<!-- Line breaks fall only between the children of a flex container or
     between absolutely positioned elements, where they cannot render as a
     space. Text beside an element stays on that element's line. -->
<section class="surface metric-panel scatter-panel">
  <div class="section-head">
    <div><h2>Duration vs. tokens</h2><span>Each point is one completed review. Tokens exclude cached re-reads, so engines compare.</span></div>
    <label class="colour-control">Colour by <select bind:value={colour}><option value="verdict">verdict</option><option value="model">model</option></select></label>
  </div>
  <div class="scatter" aria-label="Duration versus tokens scatter plot">
    {#each xTicks as t}
      <span class="grid v" style={`left:${t.pct}%`}></span>
      <span class="tick x" style={`left:${t.pct}%`}>{durSecs(t.value)}</span>
    {/each}
    {#each yTicks as t}
      <span class="grid h" style={`bottom:${t.pct}%`}></span>
      <span class="tick y" style={`bottom:${t.pct}%`}>{tokens(t.value)}</span>
    {/each}
    {#each dots as dot}
      <i
        class={dot.cls}
        style={`left:${dot.x}%; bottom:${dot.y}%`}
        role="img"
        aria-label={dot.label}
        on:mouseenter={() => show(dot)}
        on:mouseleave={() => (tip = null)}
      ></i>
    {/each}
    {#if tip}
      <div class="scatter-tip" style={tip.style}>
        <p class="tip-head"><i class={tip.cls}></i>{modelLabel(tip.point.model)} · {tip.point.effort || 'model default'}</p>
        <p class="tip-vals"><b>{tokens(tip.point.fresh_tokens) || '?'}</b> tokens · <b>{durSecs(tip.point.duration_secs) || '?'}</b> · {statusLabel(tip.point.verdict)}</p>
      </div>
    {/if}
    <span class="axis x">duration →</span>
    <span class="axis y">tokens →</span>
  </div>
  <div class="legend">
    {#if colour === 'model'}
      {#each [...slots] as [name, slot]}<span><i class={`model-${slot}`}></i>{modelLabel(name)}</span>{/each}
    {:else}
      <span><i class="approved"></i>approved</span>
      <span><i class="commented"></i>commented</span>
      <span><i class="changes"></i>requested changes</span>
      <span><i class="other"></i>skipped / error</span>
    {/if}
  </div>
</section>
