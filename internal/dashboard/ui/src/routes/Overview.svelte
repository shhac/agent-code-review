<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { del, fetchJSON, post } from '../lib/api';
  import { feedLive, feedStale } from '../lib/feed';
  import { ago, dur, keyOf, rel, statusKind, statusLabel, when, windowName } from '../lib/format';
  import PrIdentity from '../lib/PrIdentity.svelte';
  import type { Bucket, Candidate, Review, Run, UsageSnapshot, UsageWindow } from '../lib/types';

  let queue: Candidate[] = [];
  let reviews: Review[] = [];
  let runs: Run[] = [];
  let buckets: Bucket[] = [];
  let usageAvailable = false;
  let usage: UsageSnapshot | null = null;
  let usagePaused = false;
  let pausedReason = '';
  let addInput = '';
  let addErr = '';
  let queueShowAll = false;
  let expanded = new Set<string>();

  $: queued = queue.filter((c) => c.status === 'queued').length;
  $: reviewing = queue.filter((c) => c.status === 'reviewing').length;
  $: totalReviews = sumBuckets(buckets, 'approved') + sumBuckets(buckets, 'commented') + sumBuckets(buckets, 'requested_changes');
  $: approvedReviews = sumBuckets(buckets, 'approved');
  $: chart = chartBars(buckets);
  $: visibleQueue = queueShowAll ? queue : queue.slice(0, 100);
  $: reviewingItems = visibleQueue.filter((c) => c.status === 'reviewing');
  $: queuedItems = visibleQueue.filter((c) => c.status !== 'reviewing');
  // The rendered order: a drag operates on a draft copy so the poll can never
  // clobber an in-progress reorder, and index math happens in display space.
  $: displayQueue = draft ?? [...reviewingItems, ...queuedItems];
  $: lastRun = runs[0];

  // State is passed in explicitly: Svelte's legacy reactive statements only
  // see dependencies named in the expression, so a closure reading component
  // state silently never recomputes (the launch-week frozen-stats bug).
  function sumBuckets(bs: Bucket[], k: keyof Pick<Bucket, 'approved' | 'commented' | 'requested_changes'>) {
    return bs.reduce((n, b) => n + b[k], 0);
  }

  function historyFor(rs: Review[], c: Candidate) {
    return rs.filter((r) => r.repo === c.repo && r.number === c.number);
  }

  function chartBars(bs: Bucket[]) {
    const total = bs.reduce((n, b) => n + b.approved + b.commented + b.requested_changes, 0);
    const max = Math.max(1, ...bs.map((b) => b.approved + b.commented + b.requested_changes));
    return { total, max };
  }

  async function refresh() {
    if (dragKey) return; // never yank the list out from under a drag
    try {
      const [q, rv, rn, us, st] = await Promise.all([
        fetchJSON('/api/queue'),
        fetchJSON('/api/reviews?limit=100'),
        fetchJSON('/api/runs'),
        fetchJSON('/api/usage'),
        fetchJSON('/api/stats'),
      ]);
      queue = q.candidates || [];
      reviews = rv.reviews || [];
      runs = rn.runs || [];
      usageAvailable = !!us.available;
      usage = us.usage || null;
      usagePaused = !!us.review_paused;
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

  async function removeCandidate(c: Candidate) {
    addErr = '';
    try {
      await del('/api/queue', { repo: c.repo, number: c.number });
      await refresh();
    } catch (e: any) {
      addErr = e.message;
    }
  }

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

  function moveByKey(list: Candidate[], fromKey: string, toKey: string) {
    const from = list.findIndex((c) => keyOf(c) === fromKey);
    const to = list.findIndex((c) => keyOf(c) === toKey);
    if (from < 0 || to < 0 || from === to) return list;
    const next = [...list];
    next.splice(to, 0, ...next.splice(from, 1));
    return next;
  }

  function reorderPayload(list: Candidate[]) {
    return list.filter((c) => c.status !== 'reviewing').map((c) => ({ repo: c.repo, number: c.number }));
  }

  function dragStart(e: DragEvent, c: Candidate) {
    draft = [...reviewingItems, ...queuedItems];
    dragKey = keyOf(c);
    e.dataTransfer?.setData('text/plain', dragKey);
    if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move';
  }

  function dragOverRow(e: DragEvent, target: Candidate) {
    if (!draft || !dragKey || target.status === 'reviewing' || keyOf(target) === dragKey) return;
    e.preventDefault();
    draft = moveByKey(draft, dragKey, keyOf(target));
  }

  async function dragEnd() {
    if (!dragKey || !draft) return;
    const order = reorderPayload(draft);
    dragKey = null;
    draft = null;
    if (!order.length) return;
    addErr = '';
    try {
      await post('/api/queue/reorder', { order });
    } catch (e: any) {
      addErr = e.message;
    }
    await refresh();
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

<section class="hero">
  <div>
    <p class="eyebrow">Review dispatch</p>
    <h1>Queue</h1>
    <p>{queued} queued · {reviewing} reviewing · {totalReviews} reviews in the last 24h{#if usagePaused} · <span class="status warn"><i></i>reviews paused (usage floor)</span>{/if}</p>
  </div>
  <form class="add" on:submit|preventDefault={addToQueue}>
    <input bind:value={addInput} placeholder="owner/repo/pull/123 or GitHub PR URL" required />
    <button type="submit">Queue PR</button>
    {#if addErr}<span class="err">{addErr}</span>{/if}
  </form>
</section>

<div class="overview">
  <section class="queue-board">
    <div class="section-head">
      <div>
        <p class="eyebrow">Worklist</p>
        <h2>Pull requests</h2>
      </div>
      <span>{queue.length ? `${queued} queued · ${reviewing} reviewing` : 'empty'}</span>
    </div>

    {#if displayQueue.length}
      <div class="queue-list">
        {#each displayQueue as c, i (keyOf(c))}
          {#if i === reviewingItems.length && reviewingItems.length && queuedItems.length}
            <div class="list-divider"><span>up next</span></div>
          {/if}
          <article
            class:open={expanded.has(keyOf(c))}
            class:dragging={dragKey === keyOf(c)}
            class="ticket"
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
              {#if c.status !== 'queued'}
                <span class="status {statusKind(c.status)}"><i></i>{statusLabel(c.status)}</span>
              {/if}
            </div>
            <div class="ticket-actions">
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
                  <div><dt>Queue position</dt><dd>{c.queue_pos}</dd></div>
                </dl>
                <div class="review-history">
                  <h3>Reviews of this PR</h3>
                  {#if historyFor(reviews, c).length}
                    {#each historyFor(reviews, c) as r}
                      <p>
                        <span class="status {statusKind(r.verdict)}"><i></i>{statusLabel(r.verdict)}</span>
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

  <aside class="context">
    <section>
      <h2>Now</h2>
      <div class="now-grid">
        <div><strong>{queue.length}</strong><span>visible queue</span></div>
        <div><strong>{totalReviews}</strong><span>24h reviews</span></div>
        <div><strong>{approvedReviews}</strong><span>approved</span></div>
        <div><strong>{lastRun ? rel(lastRun.started_at) || 'just' : '–'}</strong><span>last run</span></div>
      </div>
      {#if lastRun}
        <p class="run-line"><span class="status {statusKind(lastRun.status)}"><i></i>{statusLabel(lastRun.status)}</span> {dur(lastRun.started_at, lastRun.finished_at)} on {lastRun.host}</p>
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
    </section>

    <section>
      <div class="section-head compact"><h2>Activity</h2><span>{chart.total ? `${chart.total} total · peak ${chart.max}/h` : '24h'}</span></div>
      {#if chart.total}
        <div class="bars" style={`--peak:${chart.max}`}>
          {#each buckets as b}
            <div title={`${new Date(b.hour).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })} - ${b.approved} approved, ${b.commented} commented, ${b.requested_changes} changes requested`}>
              <i class="approved" style={`height:${Math.max(2, (b.approved / chart.max) * 100)}%`}></i>
              <i class="commented" style={`height:${Math.max(2, (b.commented / chart.max) * 100)}%`}></i>
              <i class="changes" style={`height:${Math.max(2, (b.requested_changes / chart.max) * 100)}%`}></i>
            </div>
          {/each}
        </div>
        <div class="legend"><span><i class="approved"></i>Approved</span><span><i class="commented"></i>Commented</span><span><i class="changes"></i>Changes</span></div>
      {:else}
        <p class="muted">No reviews in the last 24h.</p>
      {/if}
    </section>

    <section>
      <h2>Recent runs</h2>
      {#if runs.length}
        <div class="mini-table">
          {#each runs as r}
            <p><time title={when(r.started_at)}>{ago(r.started_at)}</time><span>{dur(r.started_at, r.finished_at)}</span><span class="status {statusKind(r.status)}"><i></i>{statusLabel(r.status)}</span><span>{r.host}</span></p>
          {/each}
        </div>
      {:else}
        <p class="muted">No runs yet.</p>
      {/if}
    </section>
  </aside>
</div>
