<script lang="ts">
  import { fetchJSON } from '../lib/api';
  import { feedLive, feedStale } from '../lib/feed';
  import { durSecs, prHref, rel, statusKind, statusLabel, when } from '../lib/format';
  import { poll } from '../lib/poll';

  export let repo: string;
  export let number: number;

  type PrInfo = {
    repo: string;
    number: number;
    title?: string;
    author?: string;
    url?: string;
    verdict?: string;
    claimed_at?: string;
    reviewed_at?: string;
    duration_secs?: number;
  };

  let available = false;
  let loaded = false;
  let state = '';
  let content = '';
  let truncated = false;
  let pr: PrInfo | null = null;
  let pane: HTMLDivElement;

  async function refresh() {
    try {
      const pinned = pane ? pane.scrollHeight - pane.scrollTop - pane.clientHeight < 40 : true;
      const data = await fetchJSON(`/api/review-log?repo=${encodeURIComponent(repo)}&number=${number}`);
      available = !!data.available;
      state = data.state || '';
      content = data.content || '';
      truncated = !!data.truncated;
      pr = data.pr || null;
      loaded = true;
      feedLive(state || 'no log');
      setTimeout(() => {
        if (pinned && pane) pane.scrollTop = pane.scrollHeight;
      });
    } catch {
      feedStale();
    }
  }

  // Live reviews tail fast; finished ones only need the occasional re-read.
  poll(refresh, () => (state === 'reviewing' ? 3000 : 15000));

  // A finished review wears its verdict; in-flight states wear themselves.
  $: displayStatus = state === 'finished' ? pr?.verdict || 'finished' : state;
</script>

<section class="page-head">
  <p class="eyebrow">Review agent</p>
  <h1><a class="plain-link" href={prHref(repo, number, pr?.url)} target="_blank" rel="noopener">#{number}</a> {pr?.title || ''}</h1>
  <p>
    {repo}{pr?.author ? ` · @${pr.author}` : ''}
    {#if state}
      · <span class="status {statusKind(displayStatus)}"><i></i>{statusLabel(displayStatus)}</span>
    {/if}
    {#if state === 'reviewing' && pr?.claimed_at}
      · running for {rel(pr.claimed_at)}
    {:else if state === 'finished' && pr?.duration_secs}
      · took {durSecs(pr.duration_secs)}
    {/if}
    {#if pr?.reviewed_at}
      · completed <span title={when(pr.reviewed_at)}>{rel(pr.reviewed_at)} ago</span>
    {/if}
    {#if truncated}
      · showing the last 128KB
    {/if}
  </p>
</section>
<section class="terminal review-log" bind:this={pane}>
  {#if content}
    <pre class="raw">{content}</pre>
  {:else if loaded && !available}
    <div class="empty">No log recorded for this review (reviews before the live-log feature have none).</div>
  {:else if loaded}
    <div class="empty">The agent has not written anything yet.</div>
  {/if}
</section>
