<script lang="ts">
  import { onMount } from 'svelte';
  import { getMetrics } from '../lib/api';
  import { toggleIn } from '../lib/expandable';
  import { withFeed } from '../lib/feed';
  import { durSecs, exact, modelLabel, tokens, usd } from '../lib/format';
  import { cacheShare, costTitle, estimatedShare, metricFacets, modelKey, verdictRing, versionSummary } from '../lib/metrics';
  import MetricsActivity from '../lib/MetricsActivity.svelte';
  import MetricsScatter from '../lib/MetricsScatter.svelte';
  import type { MetricsResponse } from '../lib/types';

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
  let data: MetricsResponse | null = null;

  $: ({ models, efforts } = metricFacets(data));
  $: estShare = estimatedShare(data?.summary);
  $: costTip = costTitle(data?.summary);
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
      <MetricsActivity activity={data.activity} />
      <section class="surface metric-panel verdict-panel">
        <div class="section-head"><h2>Verdicts</h2><span>{verdictTotal} total</span></div>
        <div class="ring" style={`background:${ring}`}><b>{verdictTotal}</b></div>
        <div class="verdict-list">
          <span><i class="approved"></i>Approved <b>{approved}</b></span>
          <span><i class="commented"></i>Commented <b>{commented}</b></span>
          <span><i class="changes"></i>Requested changes <b>{rejected}</b></span>
        </div>
      </section>
    </div>
    <MetricsScatter scatter={data.scatter} />
    <section class="surface metric-panel">
      <div class="section-head"><h2>Model + effort breakdown</h2><span>expand a row for its CLI versions</span></div>
      <div class="metric-table">
        <p class="metric-table-head"><b></b><b>Model</b><b>Effort</b><b>Reviews</b><b>Tokens</b><b>Cached</b><b>Median time</b><b>Median cost</b><b>Total cost</b></p>
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
            <span>{usd(row.total_cost_usd) || '–'}</span>
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
                <span>{usd(v.total_cost_usd) || '–'}</span>
              </p>
            {/each}
          {/if}
        {/each}
      </div>
    </section>
  </div>
{/if}
