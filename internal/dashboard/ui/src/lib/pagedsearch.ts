// The cursor-paged search behind the history page, as a plain module.
//
// It lived inside History.svelte, where the interesting parts could only be
// exercised by driving a browser: the cursor-invalidation rule, the
// stale-response guard, and the optimistic render that must never present a
// partial in-memory subset as a settled answer. Those are the properties most
// worth pinning and the ones a browser test verifies weakly and slowly, so
// they live here beside poll.ts and queueorder.ts, which is where this project
// already puts logic it wants to test without a DOM.
//
// Deliberately not a Svelte store: the component owns its reactive state and
// calls in. This owns the rules.

export type PageRequest = { q: string; cursor: string; limit: number };
export type PageResult<T> = { rows: T[]; total: number; nextCursor?: string };

export type Snapshot<T> = {
  /** Rows to render right now. */
  rows: T[];
  /** True while what is rendered is a guess rather than the server's answer. */
  pending: boolean;
  /** Matches across the whole table, not just this page. */
  total: number;
  /** How many pages the pager should offer. */
  pageCount: number;
};

type Options<T> = {
  perPage: number;
  fetchPage: (req: PageRequest) => Promise<PageResult<T>>;
  /** Stable identity for a row, used to dedupe the in-memory pool. */
  key: (row: T) => string;
  /** Whether a row matches a query locally, for the optimistic render. */
  matches: (row: T, needle: string) => boolean;
};

export function pagedSearch<T>(opts: Options<T>) {
  const { perPage, fetchPage, key, matches } = opts;

  let query = '';
  let page = 0;
  let cursors: string[] = [''];
  let rows: T[] = [];
  let total = 0;
  let pending = false;

  // Every page we have actually been given, keyed by the pair that identifies
  // it. A revisited page renders instantly and correctly rather than as a
  // guess.
  const pageCache = new Map<string, T[]>();
  // Every row we have ever seen, so a new query has something to guess from.
  let seen: T[] = [];

  const cacheKey = (q: string, cursor: string) => `${q}\n${cursor}`;
  const cursorFor = (p: number) => cursors[p] ?? '';

  function snapshot(): Snapshot<T> {
    return { rows, pending, total, pageCount: Math.max(1, Math.ceil(total / perPage)) };
  }

  // What to show while a request is in flight: the exact page when we have it,
  // otherwise a guess from rows already in memory. Either way the screen keeps
  // content instead of blanking — but a guess is marked pending, because a
  // subset of what we happen to have loaded is not an answer.
  function optimistic(q: string, cursor: string) {
    const exact = pageCache.get(cacheKey(q, cursor));
    if (exact) {
      rows = exact;
      pending = false;
      return;
    }
    const needle = q.trim().toLowerCase();
    if (needle) rows = seen.filter((r) => matches(r, needle)).slice(0, perPage);
    pending = true;
  }

  function remember(fresh: T[]) {
    const known = new Set(seen.map(key));
    seen = seen.concat(fresh.filter((r) => !known.has(key(r))));
  }

  return {
    snapshot,

    /** A new query invalidates every cursor: they name rows in the old result
     *  set. The total goes too, or the pager offers pages this search may not
     *  have. */
    setQuery(q: string) {
      if (q === query) return false;
      query = q;
      page = 0;
      cursors = [''];
      total = 0;
      return true;
    },

    setPage(p: number) {
      if (p === page) return false;
      page = p;
      return true;
    },

    /** Render the best guess available for where we are now. */
    show() {
      optimistic(query, cursorFor(page));
      return snapshot();
    },

    /** Fetch the current page. A slower earlier request must never overwrite a
     *  later one's answer, so the reply is discarded unless the query and
     *  cursor it was asked under are still the current ones. */
    async load(): Promise<Snapshot<T>> {
      const q = query;
      const cursor = cursorFor(page);
      const res = await fetchPage({ q, cursor, limit: perPage });
      if (q !== query || cursor !== cursorFor(page)) return snapshot();
      rows = res.rows ?? [];
      total = res.total ?? rows.length;
      pending = false;
      pageCache.set(cacheKey(q, cursor), rows);
      remember(rows);
      if (res.nextCursor) cursors[page + 1] = res.nextCursor;
      return snapshot();
    },

    /** For the caller's own debounce decision: was the last change typing, or
     *  a page turn? Kept as state rather than recovered from a cache key. */
    state: () => ({ query, page, cursor: cursorFor(page) }),
  };
}
