<script lang="ts">
  import { flip } from 'svelte/animate';
  import { del, post } from './api';
  import { ago, keyOf, rel, statusKind, statusLabel, untilRel, when } from './format';
  import { navigate } from './nav';
  import PrIdentity from './PrIdentity.svelte';
  import { moveByKey, reorderPayload } from './queueorder';
  import StatusBadge from './StatusBadge.svelte';
  import type { Candidate, Review } from './types';

  export let queue: Candidate[] = [];
  export let reviews: Review[] = [];
  // Parent-bound: true while a drag is in flight so the poll loop can hold
  // off replacing the queue under the user's cursor.
  export let dragging = false;
  // Called after any mutation (remove/reorder) so the parent can refetch.
  export let onchanged: () => Promise<void>;
  // Mutation errors surface in the parent's add form, the page's one error slot.
  export let onerror: (msg: string) => void;

  let queueShowAll = false;
  let expanded = new Set<string>();

  $: queued = queue.filter((c) => c.status === 'queued').length;
  $: reviewing = queue.filter((c) => c.status === 'reviewing').length;
  $: held = queue.filter((c) => c.status === 'held').length;
  $: visibleQueue = queueShowAll ? queue : queue.slice(0, 100);
  $: reviewingItems = visibleQueue.filter((c) => c.status === 'reviewing');
  $: queuedItems = visibleQueue.filter((c) => c.status !== 'reviewing');
  // The rendered order: a drag operates on a draft copy so the poll can never
  // clobber an in-progress reorder, and index math happens in display space.
  $: displayQueue = draft ?? [...reviewingItems, ...queuedItems];

  function historyFor(rs: Review[], c: Candidate) {
    return rs.filter((r) => r.repo === c.repo && r.number === c.number);
  }

  // Every queue mutation shares one lifecycle: clear the error slot, run the
  // call, refetch, surface failures — so no handler can forget a step.
  async function mutate(op: () => Promise<unknown>) {
    onerror('');
    try {
      await op();
      await onchanged();
    } catch (e: any) {
      onerror(e.message);
    }
  }

  const removeCandidate = (c: Candidate) => mutate(() => del('/api/queue', { repo: c.repo, number: c.number }));

  // The "review now" escape hatch for held rows: clears the hold, floats the
  // PR to the top, and treats it as a manual add. Distinct from drag-reorder,
  // which only moves positions and never lifts a hold.
  const promoteCandidate = (c: Candidate) => mutate(() => post('/api/queue/promote', { repo: c.repo, number: c.number }));

  function toggleCandidate(c: Candidate) {
    const next = new Set(expanded);
    const k = keyOf(c);
    if (next.has(k)) next.delete(k);
    else next.add(k);
    expanded = next;
  }

  // Drag-and-drop reordering: the grip is the hotspot; reviewing rows are
  // pinned (not draggable, and nothing can be dropped above them because
  // dragOverRow only accepts queued targets, which all sit below).
  let draft: Candidate[] | null = null;
  let dragKey: string | null = null;
  let settledKey: string | null = null; // row that just landed — flashes once
  $: dragging = dragKey !== null;

  function dragStart(e: DragEvent, c: Candidate) {
    draft = [...displayQueue]; // begin dragging from what's on screen
    dragKey = keyOf(c);
    e.dataTransfer?.setData('text/plain', dragKey);
    if (e.dataTransfer) {
      e.dataTransfer.effectAllowed = 'move';
      // The grip is the drag source, so by default the browser's floating
      // ghost would be a screenshot of the tiny glyph. Point it at the whole
      // row instead: a semi-transparent copy of the ticket follows the
      // cursor while the in-list row becomes the placeholder slot.
      const row = (e.target as HTMLElement).closest('article');
      if (row) {
        const r = row.getBoundingClientRect();
        e.dataTransfer.setDragImage(row, e.clientX - r.left, e.clientY - r.top);
      }
    }
  }

  function dragOverRow(e: DragEvent, target: Candidate) {
    if (!draft || !dragKey || target.status === 'reviewing' || keyOf(target) === dragKey) return;
    e.preventDefault();
    draft = moveByKey(draft, dragKey, keyOf(target));
  }

  async function dragEnd() {
    if (!dragKey || !draft) return;
    const order = reorderPayload(draft);
    settledKey = dragKey;
    setTimeout(() => (settledKey = null), 700);
    dragKey = null;
    draft = null;
    if (!order.length) return;
    await mutate(() => post('/api/queue/reorder', { order }));
  }
</script>

<section class="queue-board">
  <div class="section-head">
    <div>
      <p class="eyebrow">Worklist</p>
      <h2>Pull requests</h2>
    </div>
    <span>{queue.length ? `${queued} queued · ${reviewing} reviewing${held ? ` · ${held} on hold` : ''}` : 'empty'}</span>
  </div>

  {#if displayQueue.length}
    <div class="queue-list">
      {#each displayQueue as c, i (keyOf(c))}
        <article
          class:open={expanded.has(keyOf(c))}
          class:dragging={dragKey === keyOf(c)}
          class:settled={settledKey === keyOf(c)}
          class:up-next={i === reviewingItems.length && reviewingItems.length > 0 && queuedItems.length > 0}
          class="ticket"
          animate:flip={{ duration: 160 }}
          on:dragover={(e) => dragOverRow(e, c)}
        >
          <div
            class="ticket-main"
            role="button"
            tabindex="0"
            on:click={() => toggleCandidate(c)}
            on:keydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggleCandidate(c); } }}
          >
            {#if c.status !== 'reviewing'}
              <span
                class="grip"
                draggable="true"
                title="Drag to reorder"
                aria-label="Drag to reorder"
                on:click|stopPropagation
                on:dragstart={(e) => dragStart(e, c)}
                on:dragend={dragEnd}
              >⠿</span>
            {:else}
              <span class="grip pinned"></span>
            {/if}
            <span class="chev">{expanded.has(keyOf(c)) ? '⌄' : '›'}</span>
            <PrIdentity repo={c.repo} number={c.number} url={c.url} title={c.title} author={c.author} />
            <span class="tag">{c.type}</span>
            {#if c.status === 'reviewing'}
              <a
                class="status {statusKind(c.status)} status-link"
                href={`/review/${c.repo}/${c.number}`}
                title="Open the live agent log"
                on:click|preventDefault|stopPropagation={() => navigate(`/review/${c.repo}/${c.number}`)}
              ><i></i>{statusLabel(c.status)}{c.claimed_at ? ` · ${rel(c.claimed_at)}` : ''}</a>
            {:else if c.status === 'held'}
              <span
                class="status {statusKind(c.status)}"
                title={c.hold_reason === 'cooldown'
                  ? `Reviewed recently — cooling down until ${when(c.eligible_at ?? '')}`
                  : `Updated recently — settling until ${when(c.eligible_at ?? '')}`}
              ><i></i>on hold · {statusLabel(c.hold_reason ?? '')}{untilRel(c.eligible_at) ? ` · ${untilRel(c.eligible_at)}` : ''}</span>
            {:else if c.status !== 'queued'}
              <StatusBadge status={c.status} />
            {/if}
          </div>
          <div class="ticket-actions">
            {#if c.status === 'held'}
              <button
                class="go"
                aria-label="Review now"
                title="Review now: clears the hold and floats to the top (treated as a manual add)"
                on:click={() => promoteCandidate(c)}
              >▶</button>
            {/if}
            {#if c.status !== 'reviewing'}
              <button class="danger" aria-label="Remove from queue" title="Remove from queue" on:click={() => removeCandidate(c)}>×</button>
            {/if}
          </div>
          {#if expanded.has(keyOf(c))}
            <div class="ticket-detail">
              <dl>
                <div><dt>Head SHA</dt><dd class="mono">{c.head_sha}</dd></div>
                <div><dt>PR created</dt><dd title={when(c.created_at)}>{ago(c.created_at) || 'unknown'}</dd></div>
                <div><dt>PR updated</dt><dd title={when(c.updated_at)}>{ago(c.updated_at) || 'unknown'}</dd></div>
                <div><dt>Discovered</dt><dd title={when(c.discovered_at)}>{ago(c.discovered_at) || 'unknown'}</dd></div>
                {#if c.status === 'held'}
                  <div><dt>Eligible</dt><dd title={when(c.eligible_at ?? '')}>{untilRel(c.eligible_at) ? `in ${untilRel(c.eligible_at)} (${statusLabel(c.hold_reason ?? '')})` : 'now'}</dd></div>
                {/if}
                <div><dt>Queue position</dt><dd>{c.queue_pos}</dd></div>
              </dl>
              <div class="review-history">
                <h3>Reviews of this PR</h3>
                {#if historyFor(reviews, c).length}
                  {#each historyFor(reviews, c) as r}
                    <p>
                      <StatusBadge status={r.verdict} />
                      <span class="mono">{r.engine} · {r.head_sha?.slice(0, 8)}</span>
                      {#if r.head_sha === c.head_sha}<span class="tag">current head</span>{/if}
                      <time title={when(r.reviewed_at)}>{ago(r.reviewed_at)}</time>
                    </p>
                  {/each}
                {:else}
                  <p class="muted">none recorded</p>
                {/if}
              </div>
            </div>
          {/if}
        </article>
      {/each}
    </div>
    {#if queue.length > 100}
      <button class="show" on:click={() => (queueShowAll = !queueShowAll)}>{queueShowAll ? 'Collapse' : `Show all ${queue.length}`}</button>
    {/if}
  {:else}
    <div class="empty">No queued PRs.</div>
  {/if}
</section>
