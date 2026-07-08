<script lang="ts">
  import { feed } from './lib/feed';
  import { navigate } from './lib/nav';
  import { parseReviewLogPath, reviewLogRouteKey } from './lib/reviewlog';
  import type { ReviewLogRef } from './lib/types';
  import Config from './routes/Config.svelte';
  import History from './routes/History.svelte';
  import Logs from './routes/Logs.svelte';
  import Overview from './routes/Overview.svelte';
  import Prompt from './routes/Prompt.svelte';
  import ReviewLog from './routes/ReviewLog.svelte';

  type Route = 'overview' | 'history' | 'config' | 'prompt' | 'logs' | 'review';

  let reviewRef: ReviewLogRef = { repo: '', number: 0 };

  const nav: { route: Route; label: string; path: string }[] = [
    { route: 'overview', label: 'Queue', path: '/' },
    { route: 'history', label: 'History', path: '/history' },
    { route: 'config', label: 'Config', path: '/config' },
    { route: 'prompt', label: 'Prompt', path: '/prompt' },
    { route: 'logs', label: 'Logs', path: '/logs' },
  ];

  let route: Route = routeFromPath(location.pathname);

  function routeFromPath(path: string): Route {
    const ref = parseReviewLogPath(path);
    if (ref) {
      reviewRef = ref;
      return 'review';
    }
    if (path === '/history') return 'history';
    if (path === '/config' || path === '/config.html') return 'config';
    if (path === '/prompt' || path === '/prompt.html') return 'prompt';
    if (path === '/logs' || path === '/logs.html') return 'logs';
    return 'overview';
  }

  window.addEventListener('popstate', () => {
    route = routeFromPath(location.pathname);
  });
</script>

<svelte:head>
  <title>agent-code-review · {route === 'review' ? `review #${reviewRef.number}` : nav.find((n) => n.route === route)?.label}</title>
</svelte:head>

<div class="shell">
  <aside class="rail">
    <button class="brand" type="button" on:click={() => navigate('/')}>
      <img src="/mascot.webp" alt="agent-code-review mascot" width="64" height="64" />
      <span>
        <strong>agent</strong>
        <em>code review</em>
      </span>
    </button>
    <nav aria-label="Dashboard">
      {#each nav as item}
        <a href={item.path} class:active={route === item.route} on:click|preventDefault={() => navigate(item.path)}>{item.label}</a>
      {/each}
    </nav>
    <div class:stale={!$feed.ok} class="feed">
      <span class="signal"></span>
      <span>{$feed.ok ? 'live' : 'stale'}</span>
      <small>{$feed.detail}</small>
    </div>
  </aside>

  <main>
    {#if route === 'overview'}
      <Overview />
    {:else if route === 'history'}
      <History />
    {:else if route === 'config'}
      <Config />
    {:else if route === 'prompt'}
      <Prompt />
    {:else if route === 'logs'}
      <Logs />
    {:else if route === 'review'}
      {#key reviewLogRouteKey(reviewRef)}
        <ReviewLog {reviewRef} />
      {/key}
    {/if}
  </main>
</div>
