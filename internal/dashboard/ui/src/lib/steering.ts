// One copy of the steering message limit on the client.
//
// It mirrors store.SteeringMaxLen. The server is authoritative and rejects an
// over-long message with a 400, so this only decides when the textarea stops
// accepting keystrokes and what the remaining-characters counter says.
export const MAX_STEERING = 2000;

// A steering-editor session: while somebody has the editor open, the PR is
// parked so a free dispatcher slot cannot claim it out from under them.
//
// Two rules are the whole reason this is a module rather than a few lines in
// the component, because both are easy to get subtly wrong and neither is
// reachable by a test from inside markup:
//
//   - "Should the UI claim the PR is protected" and "is there a session on the
//     server to release" are DIFFERENT questions. Conflating them meant a
//     capped or failed renewal skipped the release on close, stranding a
//     server-side session marker that then capped every later session on that
//     row instantly. So `release` is unconditional once started: the server
//     is idempotent, and the cost of a redundant DELETE is nothing next to the
//     cost of a missed one.
//   - Renewal stops when the server says to. `capped` and `disabled` both mean
//     stop asking, and a failure means stop trying; only `held` earns another
//     round. Nothing here decides how long a hold lasts: the server does, and
//     the expiry is what actually guarantees the PR is released.

import { holdForSteering, releaseSteeringHold } from './api';

export const RENEW_MS = 60_000;

export type SteeringSession = {
  /** Take the hold and keep renewing it until told to stop. */
  start: () => Promise<void>;
  /** End the session: stop renewing and release, if we ever started one. */
  release: () => Promise<void>;
  /** Whether the PR is actually parked right now, for the UI to report. */
  parked: () => boolean;
};

type Deps = {
  hold?: typeof holdForSteering;
  free?: typeof releaseSteeringHold;
  setTimer?: (fn: () => void, ms: number) => ReturnType<typeof setInterval>;
  clearTimer?: (t: ReturnType<typeof setInterval>) => void;
};

export function steeringSession(repo: string, number: number, deps: Deps = {}): SteeringSession {
  const hold = deps.hold ?? holdForSteering;
  const free = deps.free ?? releaseSteeringHold;
  const setTimer = deps.setTimer ?? ((fn, ms) => setInterval(fn, ms));
  const clearTimer = deps.clearTimer ?? ((t) => clearInterval(t));

  let timer: ReturnType<typeof setInterval> | undefined;
  let parked = false;
  // Tracked separately from `parked` on purpose: this is "the server may have
  // a session for us", which stays true after a cap or a failure even though
  // the PR is no longer protected. Set when the request is SENT, not when it
  // succeeds: a call that fails in transit may still have taken the hold, and
  // a redundant DELETE costs nothing while a missed one strands the session.
  let started = false;

  function stopRenewing() {
    if (timer !== undefined) clearTimer(timer);
    timer = undefined;
  }

  async function renew() {
    started = true;
    try {
      const res = await hold(repo, number);
      parked = res.state === 'held';
      if (res.state !== 'held') stopRenewing();
    } catch {
      // A hold is a nicety, not a precondition: failing to park must never
      // stop somebody writing. The save is what reports real refusals.
      parked = false;
      stopRenewing();
    }
  }

  return {
    async start() {
      await renew();
      if (timer === undefined && parked) timer = setTimer(() => void renew(), RENEW_MS);
    },
    async release() {
      stopRenewing();
      parked = false;
      if (!started) return;
      started = false;
      try {
        await free(repo, number);
      } catch {
        // Best effort by design: the hold expires on its own, so a release
        // that never lands costs at most one window.
      }
    },
    parked: () => parked,
  };
}
