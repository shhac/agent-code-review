// The sidebar's live/stale indicator. Route components report their fetch
// health here; the shell renders it.

import { writable } from 'svelte/store';

export type Feed = { ok: boolean; detail: string };

export const feed = writable<Feed>({ ok: true, detail: 'syncing' });

export function feedLive(detail = new Date().toLocaleTimeString()) {
  feed.set({ ok: true, detail });
}

export function feedStale(detail = 'stale') {
  feed.set({ ok: false, detail });
}

// withFeed wraps a route's refresh so the report-your-health contract can't
// be forgotten: success marks the feed live (with the returned detail, if
// any), a throw marks it stale. Register with poll()/onMount instead of
// calling feedLive/feedStale inline.
export function withFeed(fn: () => Promise<string | void> | string | void): () => Promise<void> {
  return async () => {
    try {
      const detail = await fn();
      if (typeof detail === 'string') feedLive(detail);
      else feedLive();
    } catch {
      feedStale();
    }
  };
}
