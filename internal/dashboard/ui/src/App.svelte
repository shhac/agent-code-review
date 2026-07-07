<script lang="ts">
  import { feed } from './lib/feed';
  import Config from './routes/Config.svelte';
  import History from './routes/History.svelte';
  import Logs from './routes/Logs.svelte';
  import Overview from './routes/Overview.svelte';
  import Prompt from './routes/Prompt.svelte';

  type Route = 'overview' | 'history' | 'config' | 'prompt' | 'logs';

  const nav: { route: Route; label: string; path: string }[] = [
    { route: 'overview', label: 'Queue', path: '/' },
    { route: 'history', label: 'History', path: '/history' },
    { route: 'config', label: 'Config', path: '/config' },
    { route: 'prompt', label: 'Prompt', path: '/prompt' },
    { route: 'logs', label: 'Logs', path: '/logs' },
  ];

  let route: Route = routeFromPath(location.pathname);

  function routeFromPath(path: string): Route {
    if (path === '/history') return 'history';
    if (path === '/config' || path === '/config.html') return 'config';
    if (path === '/prompt' || path === '/prompt.html') return 'prompt';
    if (path === '/logs' || path === '/logs.html') return 'logs';
    return 'overview';
  }

  function go(path: string) {
    history.pushState({}, '', path);
    route = routeFromPath(location.pathname);
  }

  window.addEventListener('popstate', () => {
    route = routeFromPath(location.pathname);
  });
</script>

<svelte:head>
  <title>agent-code-review · {nav.find((n) => n.route === route)?.label}</title>
</svelte:head>

<div class="shell">
  <aside class="rail">
    <button class="brand" type="button" on:click={() => go('/')}>
      <img src="/mascot.webp" alt="agent-code-review mascot" width="64" height="64" />
      <span>
        <strong>agent</strong>
        <em>code review</em>
      </span>
    </button>
    <nav aria-label="Dashboard">
      {#each nav as item}
        <a href={item.path} class:active={route === item.route} on:click|preventDefault={() => go(item.path)}>{item.label}</a>
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
    {/if}
  </main>
</div>
