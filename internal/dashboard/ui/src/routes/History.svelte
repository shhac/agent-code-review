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
  let total = 0;
  let query = '';
  let page = 0;
  const perPage = 25;

  // pending marks what is on screen as provisional: rows we already had, not
  // the server's answer. It drives the "searching" affordance, and it is the
  // whole safety property of the optimistic path. Showing a partial set that
  // looks settled is exactly the bug this page had, where a filter over the
  // rows in memory presented itself as a search of the whole history.
  let pending = false;

  // cursors[i] starts page i; cursors[0] is '' (the newest rows). Each fetch
  // records the cursor for the page after it, so paging forward always uses a
  // cursor the server handed us and paging back reuses one we already had.
  //
  // A cursor rather than an offset because reviews land while you read. An
  // offset counts from the top of a list that grows at the top, so one review
  // finishing between two clicks makes page 2 repeat the row page 1 ended on.
  let cursors = [''];

  // Everything fetched this session, newest-first and deduped, and every exact
  // page keyed by the request that produced it. Two caches because they answer
  // different questions: pageCache answers "have I already loaded this exact
  // page" (a revisit, which can be shown as-is), seen answers "do I know any
  // rows that match what is being typed" (a guess, which cannot).
  let seen: Review[] = [];
  const pageCache = new Map<string, Review[]>();
  const cacheKey = (q: string, cursor: string) => `${q}\n${cursor}`;

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

  // The same fields the server matches on, so a provisional result is a subset
  // of the real one rather than a different question answered locally.
  const matches = (r: Review, needle: string) =>
    `${r.repo}#${r.number} ${r.title} ${r.author} ${r.verdict}`.toLowerCase().includes(needle);

  // What to show while the request is in flight: the exact page if we have
  // loaded it before, otherwise our best guess from rows already in memory.
  // Either way the screen keeps content instead of blanking.
  function optimistic(q: string, cursor: string) {
    const exact = pageCache.get(cacheKey(q, cursor));
    if (exact) {
      reviews = exact;
      pending = false;
      return;
    }
    const needle = q.trim().toLowerCase();
    reviews = needle ? seen.filter((r) => matches(r, needle)).slice(0, perPage) : reviews;
    pending = true;
  }

  // The server matches q against repo#number, title, author and verdict over
  // the whole history, and returns how many rows matched. The browser used to
  // filter the newest 500 rows instead, which quietly meant "search the last
  // few days": a handle with 21 reviews returned the 3 inside that window.
  async function load() {
    const q = query;
    const cursor = cursors[page] ?? '';
    const rv = await getReviews({ q, limit: perPage, cursor });
    // A slower earlier request must not overwrite a later one's answer.
    if (q !== query || cursor !== (cursors[page] ?? '')) return;
    reviews = rv.reviews || [];
    total = rv.total ?? reviews.length;
    pending = false;
    pageCache.set(cacheKey(q, cursor), reviews);
    remember(reviews);
    if (rv.next_cursor) cursors[page + 1] = rv.next_cursor;
  }

  function remember(rows: Review[]) {
    const known = new Set(seen.map(rowKey));
    seen = seen.concat(rows.filter((r) => !known.has(rowKey(r))));
  }

  // Page 1 is a live view; every page past it is fixed. A cursor pins its rows,
  // so polling one can only re-fetch what the reader is part-way through.
  const refresh = () => (page === 0 ? load() : Promise.resolve());

  // A new search invalidates every cursor, since they name rows in the old
  // result set, and the old total. Clearing total rather than keeping it stops
  // the pager offering pages the new search may not have.
  let lastQuery = query;
  $: if (query !== lastQuery) {
    lastQuery = query;
    page = 0;
    cursors = [''];
    total = 0;
  }

  // Typing waits for a pause so a handle typed at speed is one request; turning
  // a page fires at once, because the click already was the intent.
  let debounce: number | undefined;
  // Seeded with the key this component mounts on, so the first load is the
  // poll's opening tick rather than the poll and this statement racing to make
  // the same request twice.
  let lastKey = cacheKey('', '');
  $: schedule(query, page);
  function schedule(q: string, p: number) {
    const key = cacheKey(q, cursors[p] ?? '');
    if (key === lastKey) return;
    const typed = q !== lastKey.split('\n')[0];
    lastKey = key;
    optimistic(q, cursors[p] ?? '');
    clearTimeout(debounce);
    debounce = window.setTimeout(() => void withFeed(load)(), typed ? 200 : 0);
  }

  $: pageCount = Math.max(1, Math.ceil(total / perPage));
  $: if (page > pageCount - 1) page = pageCount - 1;

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
        <input class="filter" type="search" placeholder="search all history: repo, #number, title, author, verdict" bind:value={query} />
        <span class="matches" class:pending>
          {#if pending}searching...{:else}{total.toLocaleString()} {query ? (total === 1 ? 'match' : 'matches') : 'reviews'}{/if}
        </span>
        <Pager bind:page {pageCount} busy={pending} />
      </span>
    </div>
    {#if reviews.length}
      <div class="review-table">
        <p class="review-head" aria-hidden="true">
          <span>PR</span><span>Outcome</span><span>Engine</span>
          <span class="num">Spend</span><span>Reviewed</span><span></span>
        </p>
        {#each reviews as r (rowKey(r))}
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
    {:else if pending}
      <div class="empty">Searching all history for "{query}"...</div>
    {:else if query}
      <div class="empty">No reviews match "{query}".</div>
    {:else}
      <div class="empty">No reviews yet.</div>
    {/if}
  </section>
</div>
