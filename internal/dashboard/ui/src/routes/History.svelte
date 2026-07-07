<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { fetchJSON } from '../lib/api';
  import { feedLive, feedStale } from '../lib/feed';
  import { ago, statusKind, statusLabel, when } from '../lib/format';
  import PrIdentity from '../lib/PrIdentity.svelte';
  import type { Review } from '../lib/types';

  let reviews: Review[] = [];
  let reviewLimit = 50;

  async function refresh() {
    try {
      const rv = await fetchJSON(`/api/reviews?limit=${reviewLimit}`);
      reviews = rv.reviews || [];
      feedLive();
    } catch {
      feedStale();
    }
  }

  function showMore() {
    reviewLimit = Math.min(500, reviewLimit + 100);
    refresh();
  }

  let timer: number | undefined;
  onMount(() => {
    refresh();
    timer = window.setInterval(refresh, 15000);
  });
  onDestroy(() => {
    if (timer) window.clearInterval(timer);
  });
</script>

<section class="page-head">
  <p class="eyebrow">Archive</p>
  <h1>Review history</h1>
  <p>Every recorded outcome: approvals, comments, change requests, skips, and errors. Newest first.</p>
</section>
<div class="stack">
  <section class="surface">
    <div class="section-head">
      <div>
        <p class="eyebrow">Outcomes</p>
        <h2>Recent reviews</h2>
      </div>
      {#if reviews.length >= reviewLimit && reviewLimit < 500}<button class="show" on:click={showMore}>Show more</button>{/if}
    </div>
    {#if reviews.length}
      <div class="review-list">
        {#each reviews as r}
          <p>
            <PrIdentity repo={r.repo} number={r.number} title={r.title} author={r.author} />
            <span class="status {statusKind(r.verdict)}"><i></i>{statusLabel(r.verdict)}</span>
            <span class="mono">{r.engine} · {r.head_sha?.slice(0, 8)}</span>
            <time title={when(r.reviewed_at)}>{ago(r.reviewed_at)}</time>
          </p>
        {/each}
      </div>
    {:else}
      <div class="empty">No reviews yet.</div>
    {/if}
  </section>
</div>
