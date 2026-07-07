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
