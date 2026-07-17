<script lang="ts">
  import { flip } from 'svelte/animate';
  import { promoteQueuedPR, removeQueuedPR, reorderQueue } from './api';
  import { keyOf } from './format';
  import { moveByKey, reorderPayload } from './queueorder';
  import QueueTicket from './QueueTicket.svelte';
  import type { Candidate, QueueCounts, Review } from './types';

  export let queue: Candidate[] = [];
  // Server-computed tallies (the same payload the Overview header reads):
  // one derivation, so the two headers cannot disagree.
  export let counts: QueueCounts = { total: 0, queued: 0, reviewing: 0, held: 0 };
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
  // call, refetch, surface failures, so no handler can forget a step.
  async function mutate(op: () => Promise<unknown>) {
    onerror('');
    try {
      await op();
      await onchanged();
    } catch (e: any) {
      onerror(e.message);
    }
  }

  const removeCandidate = (c: Candidate) => mutate(() => removeQueuedPR(c));

  // The "review now" escape hatch for held rows: clears the hold, floats the
  // PR to the top, and treats it as a manual add. Distinct from drag-reorder,
  // which only moves positions and never lifts a hold.
  const promoteCandidate = (c: Candidate) => mutate(() => promoteQueuedPR(c));

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
  let settledKey: string | null = null; // row that just landed: flashes once
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
    await mutate(() => reorderQueue(order));
  }
</script>

<section class="queue-board">
  <div class="section-head">
    <div>
      <p class="eyebrow">Worklist</p>
      <h2>Pull requests</h2>
    </div>
    <span>{queue.length ? `${counts.queued} queued · ${counts.reviewing} reviewing${counts.held ? ` · ${counts.held} on hold` : ''}` : 'empty'}</span>
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
          <QueueTicket
            {c}
            history={historyFor(reviews, c)}
            expanded={expanded.has(keyOf(c))}
            ontoggle={() => toggleCandidate(c)}
            onremove={() => removeCandidate(c)}
            onpromote={() => promoteCandidate(c)}
            ondragstart={(e) => dragStart(e, c)}
            ondragend={dragEnd}
          />
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
