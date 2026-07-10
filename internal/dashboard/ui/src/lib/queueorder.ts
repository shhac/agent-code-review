// Pure drag-reorder logic for the queue board, extracted so the splice
// semantics and the pinned-reviewing contract are unit-testable.

import { keyOf } from './format';
import type { Candidate } from './types';

// moveByKey returns list with the fromKey item re-inserted at toKey's
// position; unknown keys and no-op moves return the list unchanged.
export function moveByKey(list: Candidate[], fromKey: string, toKey: string) {
  const from = list.findIndex((c) => keyOf(c) === fromKey);
  const to = list.findIndex((c) => keyOf(c) === toKey);
  if (from < 0 || to < 0 || from === to) return list;
  const next = [...list];
  next.splice(to, 0, ...next.splice(from, 1));
  return next;
}

// reorderPayload is the /api/queue/reorder body: every reorderable row in
// display order. Reviewing rows are pinned: the endpoint rejects any order
// that mentions them, so they must be filtered out here.
export function reorderPayload(list: Candidate[]) {
  return list.filter((c) => c.status !== 'reviewing').map((c) => ({ repo: c.repo, number: c.number }));
}
