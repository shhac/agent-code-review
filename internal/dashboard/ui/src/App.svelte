<script lang="ts">
  import { feed } from './lib/feed';
  import { navigate } from './lib/nav';
  import Config from './routes/Config.svelte';
  import History from './routes/History.svelte';
  import Logs from './routes/Logs.svelte';
  import Overview from './routes/Overview.svelte';
  import Prompt from './routes/Prompt.svelte';
  import ReviewLog from './routes/ReviewLog.svelte';

  type Route = 'overview' | 'history' | 'config' | 'prompt' | 'logs' | 'review';

  const reviewPath = /^\/review\/([^/]+\/[^/]+)\/(\d+)(?:\/([^/]+))?$/;
  let reviewRepo = '';
  let reviewNumber = 0;
  let reviewKey = '';

  const nav: { route: Route; label: string; path: string }[] = [
    { route: 'overview', label: 'Queue', path: '/' },
    { route: 'history', label: 'History', path: '/history' },
    { route: 'config', label: 'Config', path: '/config' },
    { route: 'prompt', label: 'Prompt', path: '/prompt' },
    { route: 'logs', label: 'Logs', path: '/logs' },
  ];

  let route: Route = routeFromPath(location.pathname);

  function routeFromPath(path: string): Route {
    const m = reviewPath.exec(path);
    if (m) {
      reviewRepo = m[1];
      reviewNumber = Number(m[2]);
      reviewKey = m[3] || '';
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
  <title>agent-code-review · {route === 'review' ? `review #${reviewNumber}` : nav.find((n) => n.route === route)?.label}</title>
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
      {#key `${reviewRepo}#${reviewNumber}#${reviewKey}`}
        <ReviewLog repo={reviewRepo} number={reviewNumber} {reviewKey} />
      {/key}
    {/if}
  </main>
</div>
