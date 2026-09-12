<script lang="ts">
  import { toggleIn } from '../lib/expandable';
  import { errText } from '../lib/errors';
  import { getReviews, preflightPR } from '../lib/api';
  import { withFeed } from '../lib/feed';
  import { ago, durSecs, prHref, tokens, when } from '../lib/format';
  import { navigate } from '../lib/nav';
  import Pager from '../lib/Pager.svelte';
  import { pagedSearch } from '../lib/pagedsearch';
  import { poll } from '../lib/poll';
  import PrIdentity from '../lib/PrIdentity.svelte';
  import { reviewLogPathFromReview } from '../lib/reviewlog';
  import SteerAndQueue from '../lib/SteerAndQueue.svelte';
  import SteeringNote from '../lib/SteeringNote.svelte';
  import StatusBadge from '../lib/StatusBadge.svelte';
  import type { QueuePreflight, Review } from '../lib/types';

  // The paging rules — cursor invalidation, the stale-response guard, the
  // optimistic render that must never present a guess as settled — live in
  // lib/pagedsearch.ts, where they are unit-tested. This component owns the
  // reactive shell and calls in.
  const perPage = 25;
  const search = pagedSearch<Review>({
    perPage,
    key: (r) => `${r.repo}#${r.number}@${r.reviewed_at}`,
    // The same fields the server matches on, so a provisional result is a
    // subset of the real one rather than a different question answered locally.
    matches: (r, needle) =>
      `${r.repo}#${r.number} ${r.title} ${r.author} ${r.verdict}`.toLowerCase().includes(needle),
    fetchPage: async ({ q, cursor, limit }) => {
      const rv = await getReviews({ q, limit, cursor });
      return { rows: rv.reviews || [], total: rv.total ?? (rv.reviews || []).length, nextCursor: rv.next_cursor };
    },
  });

  let query = '';
  let page = 0;
  let snap = search.snapshot();
  $: reviews = snap.rows;
  $: total = snap.total;
  // pending marks what is on screen as provisional: rows we already had, not
  // the server's answer. It drives the "searching" affordance, and it is the
  // whole safety property of the optimistic path. Showing a partial set that
  // looks settled is exactly the bug this page had, where a filter over the
  // rows in memory presented itself as a search of the whole history.
  $: pending = snap.pending;
  $: pageCount = snap.pageCount;

  // Expanded rows, keyed like the queue's tickets. The table carries what you
  // scan by; everything else lives one click down, so a wide row never has to
  // choose between being complete and being skimmable.
  let expanded = new Set<string>();
  const rowKey = (r: Review) => `${r.repo}#${r.number}@${r.reviewed_at}`;
  const toggle = (r: Review) => (expanded = toggleIn(expanded, rowKey(r)));

  // Reviewing a PR again from here, rather than making somebody copy the link,
  // navigate to the queue and retype what they already told it last time. The
  // instruction from the previous review prefills the editor, because "run it
  // again, but say this differently" is the whole reason to be on this row.
  //
  // history stores no url (the queue row that had one is long gone), so the
  // reference is rebuilt from repo and number — the same form the add box
  // accepts.
  let requeue: { pr: QueuePreflight; url: string; initial: string } | null = null;
  let requeueBusy = '';
  let note = '';

  async function openRequeue(r: Review) {
    note = '';
    requeueBusy = rowKey(r);
    const url = prHref(r.repo, r.number);
    try {
      // Resolve first, so the modal can say whether this viewer may steer and
      // refuse the instruction with a reason rather than silently dropping it.
      requeue = { pr: await preflightPR(url), url, initial: r.steering?.message ?? '' };
    } catch (e) {
      note = errText(e);
    } finally {
      requeueBusy = '';
    }
  }

  // Page 1 is a live view; every page past it is fixed. A cursor pins its rows,
  // so polling one can only re-fetch what the reader is part-way through.
  const load = async (): Promise<void> => {
    snap = await search.load();
  };
  const refresh = (): Promise<void> => (page === 0 ? load() : Promise.resolve());

  // Typing waits for a pause so a handle typed at speed is one request; turning
  // a page fires at once, because the click already was the intent.
  let debounce: number | undefined;
  let started = false;
  $: apply(query, page);
  function apply(q: string, p: number) {
    const typed = search.setQuery(q);
    const turned = search.setPage(p);
    // Seeded so the first render is the poll's opening tick rather than this
    // statement and the poll racing to make the same request twice.
    if (!started) {
      started = true;
      return;
    }
    if (!typed && !turned) return;
    snap = search.show();
    clearTimeout(debounce);
    debounce = window.setTimeout(() => void withFeed(load)(), typed ? 200 : 0);
  }

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
          {#if pending}searching...{:else}{total.toLocaleString()} {query ? (total === 1 ? 'match' : 'matches') : 'outcomes'}{/if}
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
              <span class="pr-cell"><PrIdentity repo={r.repo} number={r.number} title={r.title} author={r.author} score={r.score} bucket={r.score_bucket ?? ''} /></span>
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
                {#if r.steering}
                  <div class="steering-past">
                    <h3>Steering it was given</h3>
                    <SteeringNote steering={r.steering} />
                  </div>
                {/if}
                <div class="detail-actions">
                  {#if reviewLogPathFromReview(r)}
                    <a class="log-link" href={reviewLogPathFromReview(r)} on:click|preventDefault|stopPropagation={() => navigate(reviewLogPathFromReview(r))}>Open the review log →</a>
                  {/if}
                  <button
                    class="go"
                    disabled={requeueBusy === rowKey(r)}
                    title="Queue this PR for another review, with the option to change its steering"
                    on:click|stopPropagation={() => openRequeue(r)}
                  >{requeueBusy === rowKey(r) ? 'checking…' : 'Review again'}</button>
                </div>
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

{#if note}<p class="status bad note"><i></i>{note}</p>{/if}

{#if requeue}
  <SteerAndQueue
    pr={requeue.pr}
    url={requeue.url}
    title={`Review ${requeue.pr.repo}#${requeue.pr.number} again`}
    initial={requeue.initial}
    onclose={() => (requeue = null)}
    ondone={(n) => (note = n)}
  />
{/if}

<style>
  .steering-past { margin-top: 14px; }
  .steering-past h3 {
    margin: 0 0 6px; font-size: 13px; text-transform: uppercase;
    letter-spacing: .04em; color: var(--dim);
  }
  .detail-actions {
    display: flex; gap: 12px; align-items: center;
    flex-wrap: wrap; margin-top: 14px;
  }
  /* The action sits at the far edge, away from the link it is not a sibling
     of in intent: one navigates, the other spends money. margin-left rather
     than space-between, because the log link is conditional and a lone button
     must still end up on the right. */
  .detail-actions .go { margin-left: auto; }
  .note { margin: 12px 0 0; }
</style>
