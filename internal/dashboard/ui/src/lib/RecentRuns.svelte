<script lang="ts">
  import { ago, dur, statusKind, statusLabel, when } from './format';
  import type { Run } from './types';

  export let runs: Run[] = [];

  const runsPerPage = 5;
  let runsPage = 0;

  // One source for the pager bound; the clamp keeps the page valid when a
  // poll shrinks the list.
  $: pageCount = Math.max(1, Math.ceil(runs.length / runsPerPage));
  $: if (runsPage > pageCount - 1) runsPage = pageCount - 1;
</script>

<section>
  <div class="section-head compact">
    <h2>Recent runs</h2>
    {#if runs.length > runsPerPage}
      <span class="pager">
        <button type="button" disabled={runsPage === 0} on:click={() => (runsPage -= 1)}>‹</button>
        {runsPage + 1}/{pageCount}
        <button type="button" disabled={runsPage >= pageCount - 1} on:click={() => (runsPage += 1)}>›</button>
      </span>
    {/if}
  </div>
  {#if runs.length}
    <div class="mini-table">
      {#each runs.slice(runsPage * runsPerPage, (runsPage + 1) * runsPerPage) as r}
        <p><time title={when(r.started_at)}>{ago(r.started_at)}</time><span>{dur(r.started_at, r.finished_at)}</span><span class="status {statusKind(r.status)}"><i></i>{statusLabel(r.status)}</span><span>{r.host}</span></p>
      {/each}
    </div>
  {:else}
    <p class="muted">No runs yet.</p>
  {/if}
</section>
