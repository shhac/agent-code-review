<script lang="ts">
  import { getReviewLog } from '../lib/api';
  import { parseCodexLog, verdictShaped } from '../lib/codexlog';
  import { feedLive, feedStale } from '../lib/feed';
  import { durSecs, prHref, rel, tokens, when } from '../lib/format';
  import { poll } from '../lib/poll';
  import StatusBadge from '../lib/StatusBadge.svelte';
  import type { ReviewLogPr } from '../lib/types';

  export let repo: string;
  export let number: number;
  export let reviewKey = '';

  let available = false;
  let loaded = false;
  let state = '';
  let content = '';
  let truncated = false;
  let pr: ReviewLogPr | null = null;
  let pane: HTMLDivElement;
  let showRaw = false;

  async function refresh() {
    try {
      const pinned = pane ? pane.scrollHeight - pane.scrollTop - pane.clientHeight < 40 : true;
      const data = await getReviewLog(repo, number, reviewKey);
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

  // One bubble per stream event; null means the content isn't a codex
  // stream, so the raw view is the only view.
  $: events = content ? parseCodexLog(content) : null;

  const lineCount = (s: string) => s.split('\n').length;
  const kindLabel: Record<string, string> = {
    meta: 'session',
    user: 'prompt',
    thinking: 'thinking',
    codex: 'agent',
    tokens: 'tokens used',
  };
  // The tokens trailer renders as a meta-styled bubble; everything else wears
  // its own kind as the style class.
  const bubbleClass: Record<string, string> = { tokens: 'meta' };
</script>

<section class="page-head">
  <p class="eyebrow">Review agent</p>
  <h1 class="review-log-heading">
    <a class="plain-link" href={prHref(repo, number, pr?.url)} target="_blank" rel="noopener">#{number}</a>
    {#if pr?.title}<span>{pr.title}</span>{/if}
  </h1>
  <p>
    {repo}{pr?.author ? ` · @${pr.author}` : ''}
    {#if state}
      · <StatusBadge status={displayStatus} />
    {/if}
    {#if state === 'reviewing' && pr?.claimed_at}
      · running for {rel(pr.claimed_at)}
    {:else if state === 'finished' && pr?.duration_secs}
      · took {durSecs(pr.duration_secs)}
    {/if}
    {#if pr?.tokens_used}
      · {tokens(pr.tokens_used)} tokens
    {/if}
    {#if pr?.reviewed_at}
      · completed <span title={when(pr.reviewed_at)}>{rel(pr.reviewed_at)} ago</span>
    {/if}
    {#if truncated}
      · showing the last 128KB
    {/if}
    {#if events}
      · <button class="log-view-toggle" type="button" on:click={() => (showRaw = !showRaw)}>{showRaw ? 'bubbles' : 'raw'}</button>
    {/if}
  </p>
</section>
<section class="terminal review-log" bind:this={pane}>
  {#if content && events && !showRaw}
    <div class="log-events">
      {#each events as ev}
        {#if ev.kind === 'exec'}
          <article class="log-bubble exec" class:failed={ev.ok === false}>
            <header>
              <span class="kind">exec</span>
              {#if ev.ok === undefined}
                <span class="status info live"><i></i>running</span>
              {:else}
                <span class="status {ev.ok ? 'ok' : 'bad'}"><i></i>{ev.ok ? 'ok' : 'failed'}{ev.duration ? ` · ${ev.duration}` : ''}</span>
              {/if}
            </header>
            <pre class="cmd">{ev.command}</pre>
            {#if ev.output}
              {#if lineCount(ev.output) > 8}
                <details>
                  <summary>output · {lineCount(ev.output)} lines</summary>
                  <pre>{ev.output}</pre>
                </details>
              {:else}
                <pre class="out">{ev.output}</pre>
              {/if}
            {/if}
          </article>
        {:else if ev.kind === 'user' || ev.kind === 'meta'}
          <article class="log-bubble {ev.kind}">
            <details>
              <summary><span class="kind">{kindLabel[ev.kind]}</span> {lineCount(ev.body)} lines</summary>
              <pre>{ev.body}</pre>
            </details>
          </article>
        {:else}
          {@const verdict = ev.kind === 'codex' ? verdictShaped(ev.body) : null}
          <article class="log-bubble {bubbleClass[ev.kind] ?? ev.kind}">
            <header>
              <span class="kind">{kindLabel[ev.kind]}</span>
              {#if verdict && verdict.decision !== 'WORKING'}<span class="decision">decision: {verdict.decision}</span>{/if}
            </header>
            <p>{verdict ? verdict.summary : ev.body}</p>
          </article>
        {/if}
      {/each}
    </div>
  {:else if content}
    <pre class="raw">{content}</pre>
  {:else if loaded && !available}
    <div class="empty">No log recorded for this review.</div>
  {:else if loaded}
    <div class="empty">The agent has not written anything yet.</div>
  {/if}
</section>
