<script lang="ts">
  import { ago, rel, statusKind, statusLabel, untilRel, when } from './format';
  import { navigate } from './nav';
  import PrIdentity from './PrIdentity.svelte';
  import { liveReviewLogRef, reviewLogPath } from './reviewlog';
  import StatusBadge from './StatusBadge.svelte';
  import type { Candidate, Review } from './types';

  // One queue row's content: main line, status variant, actions, and the
  // expandable detail. The article shell (keyed flip animation, dragover
  // target, open/dragging classes) stays in QueueBoard, which owns all
  // list-level state; this component receives row-scoped data + callbacks.
  export let c: Candidate;
  export let history: Review[] = [];
  export let expanded = false;
  export let ontoggle: () => void;
  export let onremove: () => void;
  export let onpromote: () => void;
  export let ondragstart: (e: DragEvent) => void;
  export let ondragend: () => void;
</script>

<div
  class="ticket-main"
  role="button"
  tabindex="0"
  on:click={ontoggle}
  on:keydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); ontoggle(); } }}
>
  {#if c.status !== 'reviewing'}
    <button
      type="button"
      class="grip"
      draggable="true"
      title="Drag to reorder"
      aria-label="Drag to reorder"
      on:click|stopPropagation
      on:dragstart={ondragstart}
      on:dragend={ondragend}
    >⠿</button>
  {:else}
    <span class="grip pinned"></span>
  {/if}
  <span class="chev">{expanded ? '⌄' : '›'}</span>
  <PrIdentity repo={c.repo} number={c.number} url={c.url} title={c.title} author={c.author} />
  <span class="tag">{c.type}</span>
  {#if c.status === 'reviewing'}
    {@const logRef = liveReviewLogRef(c)}
    <a
      class="status {statusKind(c.status)} status-link"
      href={reviewLogPath(logRef)}
      title="Open the live agent log"
      on:click|preventDefault|stopPropagation={() => navigate(reviewLogPath(logRef))}
    ><i></i>{statusLabel(c.status)}{c.claimed_at ? ` · ${rel(c.claimed_at)}` : ''}</a>
  {:else if c.status === 'held'}
    <span
      class="status {statusKind(c.status)}"
      title={c.hold_reason === 'cooldown'
        ? `Reviewed recently, cooling down until ${when(c.eligible_at ?? '')}`
        : `Updated recently, settling until ${when(c.eligible_at ?? '')}`}
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
      on:click={onpromote}
    >▶</button>
  {/if}
  {#if c.status !== 'reviewing'}
    <button class="danger" aria-label="Remove from queue" title="Remove from queue" on:click={onremove}>×</button>
  {/if}
</div>
{#if expanded}
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
      {#if history.length}
        {#each history as r}
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
