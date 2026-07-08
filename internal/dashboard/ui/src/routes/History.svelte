<script lang="ts">
  import { fetchJSON } from '../lib/api';
  import { feedLive, feedStale } from '../lib/feed';
  import { ago, durSecs, statusKind, statusLabel, tokens, when } from '../lib/format';
  import { navigate } from '../lib/nav';
  import { poll } from '../lib/poll';
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

  poll(refresh, 15000);
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
            <span class="dur">{durSecs(r.duration_secs)}{r.tokens_used ? ` · ${tokens(r.tokens_used)} tok` : ''}</span>
            <time title={when(r.reviewed_at)}>{ago(r.reviewed_at)}</time>
            <span>
              {#if r.work_dir}
                <a class="log-link" href={`/review/${r.repo}/${r.number}`} on:click|preventDefault={() => navigate(`/review/${r.repo}/${r.number}`)}>log</a>
              {/if}
            </span>
          </p>
        {/each}
      </div>
    {:else}
      <div class="empty">No reviews yet.</div>
    {/if}
  </section>
</div>
