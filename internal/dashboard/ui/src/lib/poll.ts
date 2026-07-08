// Shared route polling: run refresh on mount, then again every intervalMs.
// Call during component init (it registers mount/destroy hooks). A getter
// interval is read before each wait so a route can vary its cadence between
// rounds (ReviewLog tails live reviews faster); the timeout chain (rather
// than setInterval) also keeps a slow fetch from overlapping itself.

import { onDestroy, onMount } from 'svelte';

export function poll(refresh: () => void | Promise<void>, intervalMs: number | (() => number)) {
  const next = typeof intervalMs === 'function' ? intervalMs : () => intervalMs;
  let timer: number | undefined;
  let stopped = false;
  async function tick() {
    try {
      await refresh();
    } finally {
      if (!stopped) timer = window.setTimeout(tick, next());
    }
  }
  onMount(() => {
    void tick();
  });
  onDestroy(() => {
    stopped = true;
    if (timer !== undefined) window.clearTimeout(timer);
  });
}
