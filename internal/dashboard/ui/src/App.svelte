<script lang="ts">
  import { feed } from './lib/feed';
  import { onMount } from 'svelte';
  import { getConfig } from './lib/api';
  import { poll } from './lib/poll';
  import { refreshViewer, viewer } from './lib/viewer';
  import ViewerChip from './lib/ViewerChip.svelte';
  import { navigate } from './lib/nav';
  import { parseReviewLogPath, reviewLogRouteKey } from './lib/reviewlog';
  import type { ReviewLogRef } from './lib/types';
  import Config from './routes/Config.svelte';
  import History from './routes/History.svelte';
  import Leaderboard from './routes/Leaderboard.svelte';
  import Logs from './routes/Logs.svelte';
  import Metrics from './routes/Metrics.svelte';
  import Overview from './routes/Overview.svelte';
  import Prompt from './routes/Prompt.svelte';
  import ReviewLog from './routes/ReviewLog.svelte';

  type Route = 'overview' | 'history' | 'metrics' | 'leaderboard' | 'config' | 'prompt' | 'logs' | 'review';

  let reviewRef: ReviewLogRef = { repo: '', number: 0 };

  // Leaderboard is conditional: scoring fully disabled hides it rather than
  // offering a page that can only say it is switched off. Optimistic until the
  // config arrives, so the entry does not visibly appear a moment after load
  // on the overwhelmingly common path where it is on.
  let leaderboardVisible = true;

  const allNav: { route: Route; label: string; path: string }[] = [
    { route: 'overview', label: 'Queue', path: '/' },
    { route: 'history', label: 'History', path: '/history' },
    { route: 'metrics', label: 'Metrics', path: '/metrics' },
    { route: 'leaderboard', label: 'Leaderboard', path: '/leaderboard' },
    { route: 'config', label: 'Config', path: '/config' },
    { route: 'prompt', label: 'Prompt', path: '/prompt' },
    { route: 'logs', label: 'Logs', path: '/logs' },
  ];

  // Routing matches against EVERY entry, not the visible ones: a hidden page
  // reached by its URL should still render and explain itself, rather than
  // silently redirecting somewhere the reader did not ask for.
  const nav = allNav;
  $: visibleNav = allNav.filter((n) => n.route !== 'leaderboard' || leaderboardVisible);

  // Route matching derives from the nav table above (with a uniform ".html"
  // alias for every entry) so adding a route is one table row, not a second
  // mapping that can drift.
  function routeFromPath(path: string): { route: Route; reviewRef?: ReviewLogRef } {
    const ref = parseReviewLogPath(path);
    if (ref) return { route: 'review', reviewRef: ref };
    const hit = nav.find((n) => path === n.path || path === n.path + '.html');
    return { route: hit?.route ?? 'overview' };
  }

  function applyPath(path: string) {
    const matched = routeFromPath(path);
    if (matched.reviewRef) reviewRef = matched.reviewRef;
    route = matched.route;
  }

  let route: Route = 'overview';
  applyPath(location.pathname);

  window.addEventListener('popstate', () => {
    applyPath(location.pathname);
  });

  // Identity is polled, not fetched once: adding someone's tailscale_login to
  // the roster should reach them while they are looking at the page.
  poll(refreshViewer, 30000);

  // One fetch, not a poll: whether scoring is switched off changes when
  // somebody edits config.json, which is not something a rail needs to notice
  // mid-session.
  onMount(async () => {
    try {
      leaderboardVisible = (await getConfig()).scoring.leaderboard_visible;
    } catch {
      // An unreachable API is the feed indicator's job to report. Leaving the
      // entry visible is the better failure: a page that says scoring is off
      // beats a rail that silently lost an entry.
    }
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
      {#each visibleNav as item}
        <a href={item.path} class:active={route === item.route} on:click|preventDefault={() => navigate(item.path)}>{item.label}</a>
      {/each}
    </nav>
    <ViewerChip viewer={$viewer} />
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
    {:else if route === 'metrics'}
      <Metrics />
    {:else if route === 'leaderboard'}
      <Leaderboard />
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
