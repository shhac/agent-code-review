<script lang="ts">
  import { getLogs } from '../lib/api';
  import { feedLive, feedStale } from '../lib/feed';
  import { when } from '../lib/format';
  import { poll } from '../lib/poll';

  let logsAvailable = true;
  let logEntries: any[] = [];
  let logPane: HTMLDivElement;

  async function refresh() {
    try {
      const pinned = logPane ? logPane.scrollHeight - logPane.scrollTop - logPane.clientHeight < 40 : true;
      const data = await getLogs();
      logsAvailable = !!data.available;
      logEntries = data.entries || [];
      feedLive(`${logEntries.length} lines`);
      setTimeout(() => {
        if (pinned && logPane) logPane.scrollTop = logPane.scrollHeight;
      });
    } catch {
      feedStale();
    }
  }

  poll(refresh, 5000);
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
