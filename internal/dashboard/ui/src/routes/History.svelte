<script lang="ts">
  import { getReviews } from '../lib/api';
  import { withFeed } from '../lib/feed';
  import { ago, durSecs, tokens, when } from '../lib/format';
  import { navigate } from '../lib/nav';
  import Pager from '../lib/Pager.svelte';
  import { poll } from '../lib/poll';
  import PrIdentity from '../lib/PrIdentity.svelte';
  import { reviewLogPathFromReview } from '../lib/reviewlog';
  import StatusBadge from '../lib/StatusBadge.svelte';
  import type { Review } from '../lib/types';

  let reviews: Review[] = [];
  let query = '';
  let page = 0;
  const perPage = 25;

  // Expanded rows, keyed like the queue's tickets. The table carries what you
  // scan by; everything else lives one click down, so a wide row never has to
  // choose between being complete and being skimmable.
  let expanded = new Set<string>();
  const rowKey = (r: Review) => `${r.repo}#${r.number}@${r.reviewed_at}`;
  function toggle(r: Review) {
    const k = rowKey(r);
    expanded.has(k) ? expanded.delete(k) : expanded.add(k);
    expanded = expanded;
  }

  async function refresh() {
    const rv = await getReviews(500);
    reviews = rv.reviews || [];
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

  poll(withFeed(refresh), 15000);
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
      <div class="review-table">
        <p class="review-head" aria-hidden="true">
          <span>PR</span><span>Outcome</span><span>Engine</span>
          <span class="num">Spend</span><span>Reviewed</span><span></span>
        </p>
        {#each pageRows as r (rowKey(r))}
          {@const open = expanded.has(rowKey(r))}
          <div class="review-row-wrap" class:open>
            <p
              class="review-row"
              role="button"
              tabindex="0"
              aria-expanded={open}
              on:click={() => toggle(r)}
              on:keydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggle(r); } }}
            >
              <span class="pr-cell"><PrIdentity repo={r.repo} number={r.number} title={r.title} author={r.author} /></span>
              <StatusBadge status={r.verdict} />
              <span class="mono">{r.engine}</span>
              <span class="num">{durSecs(r.duration_secs)}{#if r.tokens_used}<small>{tokens(r.tokens_used)} tok</small>{/if}</span>
              <time title={when(r.reviewed_at)}>{ago(r.reviewed_at)}</time>
              <span class="chev" aria-hidden="true">{open ? '▾' : '▸'}</span>
            </p>
            {#if open}
              <div class="review-detail">
                <dl>
                  <div><dt>Head SHA</dt><dd class="mono">{r.head_sha}</dd></div>
                  <div><dt>Reviewed</dt><dd>{when(r.reviewed_at)}</dd></div>
                  <div><dt>Duration</dt><dd>{durSecs(r.duration_secs) || 'unknown'}</dd></div>
                  {#if r.model}<div><dt>Model</dt><dd class="mono">{r.model}{#if r.effort}{' · ' + r.effort}{/if}</dd></div>{/if}
                  {#if r.tokens_used}<div><dt>Tokens</dt><dd>{tokens(r.tokens_used)}</dd></div>{/if}
                  {#if r.cost_usd}
                    <div><dt>Cost</dt><dd>${r.cost_usd.toFixed(4)}{#if r.cost_estimated} <span class="tag-mute">estimated</span>{/if}</dd></div>
                  {/if}
                </dl>
                {#if reviewLogPathFromReview(r)}
                  <a class="log-link" href={reviewLogPathFromReview(r)} on:click|preventDefault|stopPropagation={() => navigate(reviewLogPathFromReview(r))}>Open the review log →</a>
                {/if}
              </div>
            {/if}
          </div>
        {/each}
      </div>
    {:else if reviews.length}
      <div class="empty">No reviews match "{query}".</div>
    {:else}
      <div class="empty">No reviews yet.</div>
    {/if}
  </section>
</div>
