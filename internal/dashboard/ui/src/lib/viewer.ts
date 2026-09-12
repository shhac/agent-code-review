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

/**
 * Whether a GitHub handle belongs to the person looking at the page.
 *
 * Here rather than beside any one page's derivations, because this is the
 * module that declares itself the single place identity is interpreted, and
 * the server's own copy of this rule (identity.go) carries a comment about the
 * predicate having drifted into three copies across two languages within an
 * hour of being written. One client-side home makes a fourth less likely.
 *
 * Handles are case-insensitive and the roster stores whatever case it was
 * given, so a raw comparison would fail to recognise somebody by the
 * capitalisation of their own name. An unidentified viewer owns nothing: this
 * must never answer true for the empty handle, or it would claim every row
 * whose author was not recorded.
 *
 * This is OWNERSHIP, not permission. The queue's may_steer is authorisation
 * and is true for an operator on every PR, so it is not a substitute.
 */
export function isViewer(author: string, v: Viewer | null): boolean {
  if (!v?.handle || !author) return false;
  return author.toLowerCase() === v.handle.toLowerCase();
}
