<script lang="ts">
  import { fetchJSON } from '../lib/api';
  import { feedLive, feedStale } from '../lib/feed';
  import { ago, durSecs, tokens, when } from '../lib/format';
  import { navigate } from '../lib/nav';
  import Pager from '../lib/Pager.svelte';
  import { poll } from '../lib/poll';
  import PrIdentity from '../lib/PrIdentity.svelte';
  import StatusBadge from '../lib/StatusBadge.svelte';
  import type { Review } from '../lib/types';

  let reviews: Review[] = [];
  let query = '';
  let page = 0;
  const perPage = 25;

  async function refresh() {
    try {
      const rv = await fetchJSON('/api/reviews?limit=500');
      reviews = rv.reviews || [];
      feedLive();
    } catch {
      feedStale();
    }
  }

  // The filter matches anywhere in "repo#number title author verdict", so
  // "#20487", a repo name, a handle, or "skipped" all work.
  $: filtered = filterReviews(reviews, query);
  $: pageCount = Math.max(1, Math.ceil(filtered.length / perPage));
  $: if (page > pageCount - 1) page = pageCount - 1;
  $: pageRows = filtered.slice(page * perPage, (page + 1) * perPage);
  $: query, (page = 0); // a new search starts from its first page

  function filterReviews(rs: Review[], q: string) {
    const needle = q.trim().toLowerCase();
    if (!needle) return rs;
    return rs.filter((r) => `${r.repo}#${r.number} ${r.title} ${r.author} ${r.verdict}`.toLowerCase().includes(needle));
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
      <span class="head-tools">
        <input class="filter" type="search" placeholder="filter: repo, #number, title, author, verdict" bind:value={query} />
        <Pager bind:page {pageCount} />
      </span>
    </div>
    {#if pageRows.length}
      <div class="review-list">
        {#each pageRows as r}
          <p>
            <PrIdentity repo={r.repo} number={r.number} title={r.title} author={r.author} />
            <StatusBadge status={r.verdict} />
            <span class="mono">{r.engine} · {r.head_sha?.slice(0, 8)}</span>
            <span class="dur">{durSecs(r.duration_secs)}{#if r.tokens_used}<small>{tokens(r.tokens_used)} tok</small>{/if}</span>
            <time title={when(r.reviewed_at)}>{ago(r.reviewed_at)}</time>
            <span>
              {#if r.work_dir}
                <a class="log-link" href={`/review/${r.repo}/${r.number}`} on:click|preventDefault={() => navigate(`/review/${r.repo}/${r.number}`)}>log</a>
              {/if}
            </span>
          </p>
        {/each}
      </div>
    {:else if reviews.length}
      <div class="empty">No reviews match "{query}".</div>
    {:else}
      <div class="empty">No reviews yet.</div>
    {/if}
  </section>
</div>
