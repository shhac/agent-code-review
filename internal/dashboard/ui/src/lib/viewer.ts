// Who the dashboard believes is looking, shared by the shell's identity chip
// and the queue's steering boxes so the two cannot disagree about it.
//
// A store rather than a prop: identity is a property of the connection, not of
// a route, and it belongs on screen everywhere. null means "not asked yet",
// which the chip renders differently from "asked, and nobody".

import { writable } from 'svelte/store';
import { getViewer } from './api';
import type { Viewer } from './types';

export const viewer = writable<Viewer | null>(null);

// refreshViewer re-reads the identity. Worth polling rather than fetching
// once: adding someone's tailscale_login to the roster should show up while
// they are looking at the page, which is exactly when they are wondering why
// they were not recognised.
export async function refreshViewer() {
  try {
    viewer.set(await getViewer());
  } catch {
    // Leave the last known identity in place. A failed poll is a connection
    // problem, which the feed indicator already reports; blanking the chip
    // would read as "you have been logged out".
  }
}
