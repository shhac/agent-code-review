<script lang="ts">
  import { onMount } from 'svelte';
  import { getMetrics } from '../lib/api';
  import { feedLive, feedStale } from '../lib/feed';
  import { durSecs, tokens } from '../lib/format';
  import type { MetricsResponse } from '../lib/types';

  let range = '30d';
  let model = '';
  let effort = '';
  let colour = 'verdict';
  let data: MetricsResponse | null = null;
  let allModels: string[] = [];
  let allEfforts: string[] = [];

  $: models = allModels;
  $: efforts = allEfforts;
  $: maxReviews = Math.max(1, ...(data?.activity || []).map((d) => d.reviews));
  $: maxTokens = Math.max(1, ...(data?.activity || []).map((d) => d.tokens_used));
  $: scatterX = Math.max(1, ...(data?.scatter || []).map((p) => p.tokens_used));
  $: scatterY = Math.max(1, ...(data?.scatter || []).map((p) => p.duration_secs));
  $: verdictTotal = Object.values(data?.verdicts || {}).reduce((a, b) => a + b, 0);
  $: approved = data?.verdicts.APPROVED || 0;
  $: commented = data?.verdicts.COMMENTED || 0;
  $: rejected = data?.verdicts.REQUESTED_CHANGES || 0;
  $: ring = `conic-gradient(var(--green) 0 ${(approved / Math.max(1, verdictTotal)) * 360}deg, var(--blue) 0 ${((approved + commented) / Math.max(1, verdictTotal)) * 360}deg, var(--red) 0 ${((approved + commented + rejected) / Math.max(1, verdictTotal)) * 360}deg, var(--amber) 0)`;

  function colourFor(point: MetricsResponse['scatter'][number]) {
    if (colour === 'model') return `model-${Math.abs(hash(point.model)) % 4}`;
    if (point.verdict === 'APPROVED') return 'approved';
    if (point.verdict === 'REQUESTED_CHANGES') return 'rejected';
    if (point.verdict === 'COMMENTED') return 'commented';
    return 'other';
  }
  function hash(s: string) { return [...s].reduce((n, c) => n + c.charCodeAt(0), 0); }
  async function load() {
    try {
      data = await getMetrics(range, model, effort);
      allModels = [...new Set([...allModels, ...data.models.map((m) => m.model).filter(Boolean)])].sort();
      allEfforts = [...new Set([...allEfforts, ...data.models.map((m) => m.effort).filter(Boolean)])].sort();
      feedLive('metrics');
    } catch { feedStale(); }
  }
  function changed() { load(); }
  onMount(load);
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
      <div><strong>{tokens(data.summary.tokens_used) || '0'}</strong><span>tokens used</span></div>
      <div><strong>{durSecs(data.summary.median_duration_secs) || '—'}</strong><span>median duration</span></div>
    </section>
    <div class="metrics-grid">
      <section class="surface metric-panel activity-panel"><div class="section-head"><h2>Completed reviews + token spend</h2><span>daily</span></div><div class="activity-plot">{#each data.activity as day}<div class="activity-day" title={`${day.day}: ${day.reviews} reviews · ${day.tokens_used} tokens`}><i class="review-bar" style={`height:${Math.max(3, day.reviews / maxReviews * 100)}%`}></i><b class="token-line" style={`bottom:${Math.max(2, day.tokens_used / maxTokens * 100)}%`}></b></div>{/each}</div><div class="legend"><span><i class="approved"></i>completed reviews</span><span><i class="commented"></i>tokens used</span></div></section>
      <section class="surface metric-panel verdict-panel"><div class="section-head"><h2>Verdicts</h2><span>{verdictTotal} total</span></div><div class="ring" style={`background:${ring}`}><b>{verdictTotal}</b></div><div class="verdict-list"><span><i class="approved"></i>Approved <b>{approved}</b></span><span><i class="commented"></i>Commented <b>{commented}</b></span><span><i class="changes"></i>Requested changes <b>{rejected}</b></span></div></section>
    </div>
    <section class="surface metric-panel scatter-panel"><div class="section-head"><div><h2>Duration vs. tokens</h2><span>Each point is one completed review.</span></div><label class="colour-control">Colour by <select bind:value={colour}><option value="verdict">verdict</option><option value="model">model</option></select></label></div><div class="scatter" aria-label="Duration versus tokens scatter plot">{#each data.scatter as point}<i class:approved={colourFor(point) === 'approved'} class:commented={colourFor(point) === 'commented'} class:rejected={colourFor(point) === 'rejected'} class:other={colourFor(point) === 'other'} class:model-0={colourFor(point) === 'model-0'} class:model-1={colourFor(point) === 'model-1'} class:model-2={colourFor(point) === 'model-2'} class:model-3={colourFor(point) === 'model-3'} style={`left:${8 + point.tokens_used / scatterX * 88}%; bottom:${8 + point.duration_secs / scatterY * 82}%`} title={`${point.model || 'Codex default'} / ${point.effort || 'default'} · ${point.verdict} · ${tokens(point.tokens_used) || 'unknown'} tokens · ${durSecs(point.duration_secs) || 'unknown duration'}`}></i>{/each}<span class="axis x">tokens →</span><span class="axis y">duration →</span></div></section>
    <section class="surface metric-panel"><div class="section-head"><h2>Model + effort breakdown</h2><span>CLI version retained per review</span></div><div class="metric-table"><p class="metric-table-head"><b>Model</b><b>Effort</b><b>Reviews</b><b>Tokens</b><b>Median</b><b>Codex</b></p>{#each data.models as row}<p><span>{row.model || 'Codex default'}</span><span>{row.effort || 'model default'}</span><span>{row.reviews}</span><span>{tokens(row.tokens_used) || '—'}</span><span>{durSecs(row.median_duration_secs) || '—'}</span><span class="mono">{row.codex_version || 'unavailable'}</span></p>{/each}</div></section>
  </div>
{/if}
