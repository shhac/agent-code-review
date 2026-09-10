// Which rows of a table are expanded.
//
// Three components each had their own version, and they differed in a way
// nobody stated: two mutated the Set in place and reassigned it so Svelte
// would notice (`expanded.has(k) ? expanded.delete(k) : expanded.add(k)`,
// a conditional evaluated purely for its side effects), while the third
// copied into a new Set. Only the copying form is safe once the Set is passed
// to a child as a prop, which is exactly what that third one does — so the
// divergence encoded a real constraint that lived nowhere.
//
// Always returns a new Set, so the safe form is the only form.
export function toggleIn<T>(set: ReadonlySet<T>, key: T): Set<T> {
  const next = new Set(set);
  if (!next.delete(key)) next.add(key);
  return next;
}
