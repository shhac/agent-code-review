<script lang="ts">
  import ActivityChart from '../lib/ActivityChart.svelte';
  import { fetchJSON, post } from '../lib/api';
  import { feedLive, feedStale } from '../lib/feed';
  import { dur, rel, tokens, windowName } from '../lib/format';
  import { poll } from '../lib/poll';
  import QueueBoard from '../lib/QueueBoard.svelte';
  import RecentRuns from '../lib/RecentRuns.svelte';
  import StatusBadge from '../lib/StatusBadge.svelte';
  import type { Bucket, Candidate, Review, Run, UsageSnapshot, UsageWindow } from '../lib/types';

  let queue: Candidate[] = [];
  let counts = { total: 0, queued: 0, reviewing: 0 };
  let reviews: Review[] = [];
  let runs: Run[] = [];
  let buckets: Bucket[] = [];
  let usageAvailable = false;
  let usage: UsageSnapshot | null = null;
  let usagePaused = false;
  let tokensTotal = 0;
  let tokens24h = 0;
  let pausedReason = '';
  let addInput = '';
  let addErr = '';
  let dragging = false;

  $: totalReviews = sumBuckets(buckets, 'approved') + sumBuckets(buckets, 'commented') + sumBuckets(buckets, 'requested_changes');
  $: approvedReviews = sumBuckets(buckets, 'approved');
  $: lastRun = runs[0];

  // State is passed in explicitly: Svelte's legacy reactive statements only
  // see dependencies named in the expression, so a closure reading component
  // state silently never recomputes (the launch-week frozen-stats bug).
  function sumBuckets(bs: Bucket[], k: keyof Pick<Bucket, 'approved' | 'commented' | 'requested_changes'>) {
    return bs.reduce((n, b) => n + b[k], 0);
  }

  async function refresh() {
    if (dragging) return; // never yank the list out from under a drag
    try {
      const [q, rv, rn, us, st] = await Promise.all([
        fetchJSON('/api/queue'),
        fetchJSON('/api/reviews?limit=100'),
        fetchJSON('/api/runs?limit=100'),
        fetchJSON('/api/usage'),
        fetchJSON('/api/stats'),
      ]);
      queue = q.candidates || [];
      counts = q.counts || { total: queue.length, queued: 0, reviewing: 0 };
      reviews = rv.reviews || [];
      runs = rn.runs || [];
      usageAvailable = !!us.available;
      usage = us.usage || null;
      usagePaused = !!us.review_paused;
      tokensTotal = us.tokens_total || 0;
      tokens24h = us.tokens_24h || 0;
      pausedReason = us.paused_reason || '';
      buckets = st.buckets || [];
      feedLive();
    } catch {
      feedStale();
    }
  }

  async function addToQueue() {
    addErr = '';
    try {
      await post('/api/queue', { url: addInput.trim() });
      addInput = '';
      await refresh();
    } catch (e: any) {
      addErr = e.message;
    }
  }

  poll(refresh, 15000);
</script>

<section class="hero">
  <div>
    <p class="eyebrow">Review dispatch</p>
    <h1>Queue</h1>
    <p>{counts.queued} queued · {counts.reviewing} reviewing · {totalReviews} reviews in the last 24h{#if usagePaused} · <span class="status warn"><i></i>reviews paused (usage floor)</span>{/if}</p>
  </div>
  <form class="add" on:submit|preventDefault={addToQueue}>
    <input bind:value={addInput} placeholder="owner/repo/pull/123 or GitHub PR URL" required />
    <button type="submit">Queue PR</button>
    {#if addErr}<span class="err">{addErr}</span>{/if}
  </form>
</section>

<div class="overview">
  <QueueBoard {queue} {reviews} bind:dragging onchanged={refresh} onerror={(msg) => (addErr = msg)} />

  <aside class="context">
    <section>
      <h2>Now</h2>
      <div class="now-grid">
        <div><strong>{counts.queued}</strong><span>waiting in queue</span></div>
        <div><strong>{totalReviews}</strong><span>24h reviews</span></div>
        <div><strong>{approvedReviews}</strong><span>approved</span></div>
        <div><strong>{lastRun ? rel(lastRun.started_at) || 'just' : '–'}</strong><span>last run</span></div>
      </div>
      {#if lastRun}
        <p class="run-line"><StatusBadge status={lastRun.status} /> {dur(lastRun.started_at, lastRun.finished_at)} on {lastRun.host}</p>
      {:else}
        <p class="muted">No runs yet.</p>
      {/if}
    </section>

    <section>
      <div class="section-head compact"><h2>Codex usage</h2>{#if usage?.plan}<span>plan {usage.plan}</span>{/if}</div>
      {#if !usageAvailable}
        <p class="muted">not available yet</p>
      {:else if usage?.error}
        <p class="muted">unavailable: {usage.error}</p>
      {:else}
        {#if usagePaused}
          <p class="status warn"><i></i>reviews paused: {pausedReason}</p>
        {/if}
        {#each [['Primary', usage?.primary], ['Secondary', usage?.secondary]] as item}
          {@const label = item[0] as string}
          {@const window = item[1] as UsageWindow | undefined}
          {#if window}
            <div class:hot={window.used_percent >= 90} class="meter">
              <div><span>{windowName(window, label)}</span><b>{Math.round(window.used_percent)}%</b></div>
              <i style={`width:${Math.min(100, Math.max(0, window.used_percent))}%`}></i>
              {#if window.resets_at}<small>resets {new Date(window.resets_at * 1000).toLocaleString()}</small>{/if}
            </div>
          {/if}
        {/each}
      {/if}
      {#if tokensTotal}
        <div class="tokens-line"><span>Tokens spent</span><b>{tokens(tokens24h) || '0'} last 24h · {tokens(tokensTotal)} all time</b></div>
      {/if}
    </section>

    <ActivityChart {buckets} />

    <RecentRuns {runs} />
  </aside>
</div>
