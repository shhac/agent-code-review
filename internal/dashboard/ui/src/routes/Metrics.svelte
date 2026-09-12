<script lang="ts">
  import { onMount } from 'svelte';
  import { getMetrics } from '../lib/api';
  import { toggleIn } from '../lib/expandable';
  import { withFeed } from '../lib/feed';
  import { durSecs, exact, maxOf, modelLabel, statusLabel, tokens, usd } from '../lib/format';
  import { cacheShare, costTitle, estimatedShare, metricFacets, modelKey, modelSlots, scatterClass, scatterPos, scatterTicksX, scatterTicksY, scatterTipStyle, trendPoints, verdictRing, versionSummary } from '../lib/metrics';
  import type { MetricsResponse } from '../lib/types';

  type ScatterPoint = MetricsResponse['scatter'][number];

  // Which model+effort rows are expanded, keyed so a refresh that reorders
  // rows (they sort by review count) cannot move one row's open state onto
  // another.
  let expandedModels = new Set<string>();

  function toggleModel(row: MetricsResponse['models'][number]) {
    // A single-version row has nothing to reveal: expanding it would restate
    // the row it came from.
    if (row.versions.length <= 1) return;
    expandedModels = toggleIn(expandedModels, modelKey(row));
  }

  let range = '30d';
  let model = '';
  let effort = '';
  let colour = 'verdict';
  let data: MetricsResponse | null = null;
  let tip: { point: ScatterPoint; cls: string; style: string } | null = null;

  $: ({ models, efforts } = metricFacets(data));
  $: maxReviews = maxOf(data?.activity || [], (d) => d.reviews);
  $: estShare = estimatedShare(data?.summary);
  $: costTip = costTitle(data?.summary);

  $: maxTokens = maxOf(data?.activity || [], (d) => d.fresh_tokens);
  $: tokenPoints = trendPoints(data?.activity || [], maxTokens);
  $: scatterDuration = maxOf(data?.scatter || [], (p) => p.duration_secs);
  $: scatterTokens = maxOf(data?.scatter || [], (p) => p.fresh_tokens);
  $: slots = modelSlots(data?.scatter || []);
  $: xTicks = scatterTicksX(scatterDuration);
  $: yTicks = scatterTicksY(scatterTokens);
  $: verdictTotal = Object.values(data?.verdicts || {}).reduce((a, b) => a + b, 0);
  $: approved = data?.verdicts.APPROVED || 0;
  $: commented = data?.verdicts.COMMENTED || 0;
  $: rejected = data?.verdicts.REQUESTED_CHANGES || 0;
  $: ring = verdictRing(data?.verdicts || {});

  async function load() {
    data = await getMetrics(range, model, effort);
    return 'metrics';
  }
  const reload = withFeed(load);
  function changed() { void reload(); }
  onMount(reload);
</script>

<section class="hero metrics-hero">
  <div><p class="eyebrow">Review intelligence</p><h1>Metrics</h1><p>Operational volume, outcomes, and review-engine provenance.</p></div>
  <div class="metrics-filters">
    <label>Range <select bind:value={range} on:change={changed}><option value="7d">7 days</option><option value="30d">30 days</option><option value="90d">90 days</option></select></label>
    <label>Model <select bind:value={model} on:change={changed}><option value="">All models</option>{#each models as value}<option value={value}>{value}</option>{/each}</select></label>
    <label>Effort <select bind:value={effort} on:change={changed}><option value="">All efforts</option>{#each efforts as value}<option value={value}>{value}</option>{/each}</select></label>
  </div>
</section>

{#if data}
  <div class="metrics-stack">
    <section class="metric-kpis">
      <div><strong>{data.summary.reviews}</strong><span>reviews completed</span></div>
      <div title="Every recorded row, including precheck skips and errors. Reviews are the subset where the engine actually posted."><strong>{data.summary.outcomes}</strong><span>outcomes recorded</span></div>
      <div><strong>{tokens(data.summary.fresh_tokens) || '0'}</strong><span>tokens processed</span></div>
      <div><strong>{durSecs(data.summary.median_duration_secs) || '–'}</strong><span>median duration</span></div>
      <div title={costTip}><strong>{usd(data.summary.median_cost_usd) || '–'}</strong><span>median cost{#if data.summary.max_cost_usd > 0}{' · peak '}{usd(data.summary.max_cost_usd)}{/if}</span></div>
      <div title={costTip}><strong>{usd(data.summary.cost_usd) || '–'}</strong><span>total cost{#if estShare}{' · '}{estShare} est.{/if}</span></div>
    </section>
    <div class="metrics-grid">
      <section class="surface metric-panel activity-panel"><div class="section-head"><h2>Completed reviews + tokens processed</h2><span>daily</span></div><div class="activity-plot"><span class="activity-axis left title">reviews</span><span class="activity-axis left top">{maxReviews}</span><span class="activity-axis left bottom">0</span><span class="activity-axis right title">tokens</span><span class="activity-axis right top">{tokens(maxTokens) || '0'}</span><span class="activity-axis right bottom">0</span>{#each data.activity as day}<div class="activity-day" title={`${day.day}: ${day.reviews} reviews · ${day.fresh_tokens} tokens`}><i class="review-bar" style={`height:${Math.max(3, day.reviews / maxReviews * 100)}%`}></i></div>{/each}<svg class="token-trend" viewBox="0 0 100 100" preserveAspectRatio="none" aria-label="Token spend trend"><polyline points={tokenPoints} /></svg></div><div class="legend"><span><i class="approved"></i>completed reviews</span><span><i class="commented"></i>tokens used</span></div></section>
      <section class="surface metric-panel verdict-panel"><div class="section-head"><h2>Verdicts</h2><span>{verdictTotal} total</span></div><div class="ring" style={`background:${ring}`}><b>{verdictTotal}</b></div><div class="verdict-list"><span><i class="approved"></i>Approved <b>{approved}</b></span><span><i class="commented"></i>Commented <b>{commented}</b></span><span><i class="changes"></i>Requested changes <b>{rejected}</b></span></div></section>
    </div>
    <section class="surface metric-panel scatter-panel"><div class="section-head"><div><h2>Duration vs. tokens</h2><span>Each point is one completed review. Tokens exclude cached re-reads, so engines compare.</span></div><label class="colour-control">Colour by <select bind:value={colour}><option value="verdict">verdict</option><option value="model">model</option></select></label></div><div class="scatter" aria-label="Duration versus tokens scatter plot">
      {#each xTicks as t}<span class="grid v" style={`left:${t.pct}%`}></span><span class="tick x" style={`left:${t.pct}%`}>{durSecs(t.value)}</span>{/each}
      {#each yTicks as t}<span class="grid h" style={`bottom:${t.pct}%`}></span><span class="tick y" style={`bottom:${t.pct}%`}>{tokens(t.value)}</span>{/each}
      {#each data.scatter as point}{@const pointClass = scatterClass(point, colour as 'verdict' | 'model', slots)}{@const pos = scatterPos(point, scatterDuration, scatterTokens)}<i class={pointClass} style={`left:${pos.x}%; bottom:${pos.y}%`} role="img" aria-label={`${modelLabel(point.model)} / ${point.effort || 'model default'} · ${statusLabel(point.verdict)} · ${tokens(point.fresh_tokens) || 'unknown'} tokens · ${durSecs(point.duration_secs) || 'unknown duration'}`} on:mouseenter={() => (tip = { point, cls: pointClass, style: scatterTipStyle(pos.x, pos.y) })} on:mouseleave={() => (tip = null)}></i>{/each}
      {#if tip}<div class="scatter-tip" style={tip.style}><p class="tip-head"><i class={tip.cls}></i>{modelLabel(tip.point.model)} · {tip.point.effort || 'model default'}</p><p class="tip-vals"><b>{tokens(tip.point.fresh_tokens) || '?'}</b> tokens · <b>{durSecs(tip.point.duration_secs) || '?'}</b> · {statusLabel(tip.point.verdict)}</p></div>{/if}
      <span class="axis x">duration →</span><span class="axis y">tokens →</span>
    </div>
    <div class="legend">{#if colour === 'model'}{#each [...slots] as [name, slot]}<span><i class={`model-${slot}`}></i>{modelLabel(name)}</span>{/each}{:else}<span><i class="approved"></i>approved</span><span><i class="commented"></i>commented</span><span><i class="changes"></i>requested changes</span><span><i class="other"></i>skipped / error</span>{/if}</div></section>
    <section class="surface metric-panel">
      <div class="section-head"><h2>Model + effort breakdown</h2><span>expand a row for its CLI versions</span></div>
      <div class="metric-table">
        <p class="metric-table-head"><b></b><b>Model</b><b>Effort</b><b>Reviews</b><b>Tokens</b><b>Cached</b><b>Median</b><b>Cost</b></p>
        {#each data.models as row (modelKey(row))}
          {@const open = expandedModels.has(modelKey(row))}
          {@const splits = row.versions.length > 1}
          <p
            class="metric-row"
            class:open
            class:expandable={splits}
            role={splits ? 'button' : undefined}
            tabindex={splits ? 0 : undefined}
            aria-expanded={splits ? open : undefined}
            title={versionSummary(row)}
            on:click={() => toggleModel(row)}
            on:keydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggleModel(row); } }}
          >
            <span class="chev" aria-hidden="true">{splits ? (open ? '▾' : '▸') : ''}</span>
            <span>{modelLabel(row.model)}</span>
            <span>{row.effort || 'model default'}</span>
            <span>{row.reviews}</span>
            <span>{tokens(row.fresh_tokens) || '–'}</span>
            <span title={row.cache_read_tokens ? `${exact(row.cache_read_tokens)} tokens re-read from cache` : 'this engine reports no cache reads'}>{cacheShare(row)}</span>
            <span>{durSecs(row.median_duration_secs) || '–'}</span>
            <span>{usd(row.median_cost_usd) || '–'}</span>
          </p>
          {#if open}
            {#each row.versions as v (v.engine_version)}
              <p class="metric-row version">
                <span></span>
                <span class="mono version-name">{v.engine_version || 'version unavailable'}</span>
                <span></span>
                <span>{v.reviews}</span>
                <span>{tokens(v.fresh_tokens) || '–'}</span>
                <span title={v.cache_read_tokens ? `${exact(v.cache_read_tokens)} tokens re-read from cache` : 'this engine reports no cache reads'}>{cacheShare(v)}</span>
                <span>{durSecs(v.median_duration_secs) || '–'}</span>
                <span>{usd(v.median_cost_usd) || '–'}</span>
              </p>
            {/each}
          {/if}
        {/each}
      </div>
    </section>
  </div>
{/if}
