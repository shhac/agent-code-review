<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { fetchJSON } from '../lib/api';
  import { feed, feedStale } from '../lib/feed';
  import { when } from '../lib/format';

  let logsAvailable = true;
  let logEntries: any[] = [];
  let logPane: HTMLDivElement;

  async function refresh() {
    try {
      const pinned = logPane ? logPane.scrollHeight - logPane.scrollTop - logPane.clientHeight < 40 : true;
      const data = await fetchJSON('/api/logs');
      logsAvailable = !!data.available;
      logEntries = data.entries || [];
      feed.set({ ok: true, detail: `${logEntries.length} lines` });
      setTimeout(() => {
        if (pinned && logPane) logPane.scrollTop = logPane.scrollHeight;
      });
    } catch {
      feedStale();
    }
  }

  let timer: number | undefined;
  onMount(() => {
    refresh();
    timer = window.setInterval(refresh, 5000);
  });
  onDestroy(() => {
    if (timer) window.clearInterval(timer);
  });
</script>

<section class="page-head">
  <p class="eyebrow">Daemon</p>
  <h1>Server logs</h1>
  <p>{logsAvailable ? `${logEntries.length} captured lines · in-memory tail` : 'log capture is not enabled in this process'}</p>
</section>
<section class="terminal" bind:this={logPane}>
  {#if logEntries.length}
    {#each logEntries as e}
      <p><time title={when(e.at)}>{new Date(e.at).toLocaleTimeString()}</time><span>{e.line}</span></p>
    {/each}
  {:else}
    <div class="empty">{logsAvailable ? 'No log lines captured yet.' : 'Log capture is not enabled in this process.'}</div>
  {/if}
</section>
