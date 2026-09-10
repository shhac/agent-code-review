import { describe, expect, it } from 'vitest';
import { pagedSearch, type PageRequest, type PageResult } from './pagedsearch';

type Row = { id: string; text: string };
const row = (id: string, text = id): Row => ({ id, text });

function harness(replies: ((req: PageRequest) => PageResult<Row>)[]) {
  const seen: PageRequest[] = [];
  let n = 0;
  const resolvers: (() => void)[] = [];
  const ps = pagedSearch<Row>({
    perPage: 2,
    key: (r) => r.id,
    matches: (r, needle) => r.text.includes(needle),
    fetchPage: (req) => {
      seen.push(req);
      const reply = replies[Math.min(n, replies.length - 1)];
      n += 1;
      // Resolve on demand so a test can interleave two in-flight requests.
      return new Promise((resolve) => resolvers.push(() => resolve(reply(req))));
    },
  });
  return { ps, seen, flush: () => resolvers.splice(0).forEach((r) => r()) };
}

describe('pagedSearch', () => {
  it('serves a revisited page from cache rather than as a guess', async () => {
    const h = harness([() => ({ rows: [row('a'), row('b')], total: 2 })]);
    const p = h.ps.load();
    h.flush();
    await p;

    h.ps.setPage(1);
    h.ps.setPage(0);
    const snap = h.ps.show();
    expect(snap.pending).toBe(false);
    expect(snap.rows.map((r) => r.id)).toEqual(['a', 'b']);
  });

  it('marks a guessed render as pending', async () => {
    const h = harness([() => ({ rows: [row('alpha'), row('beta')], total: 2 })]);
    const p = h.ps.load();
    h.flush();
    await p;

    // A query we have never fetched: whatever is shown is a subset of what we
    // happen to hold, which must not read as a settled answer.
    h.ps.setQuery('alpha');
    const snap = h.ps.show();
    expect(snap.pending).toBe(true);
    expect(snap.rows.map((r) => r.id)).toEqual(['alpha']);
  });

  it('discards a slow reply whose query is no longer current', async () => {
    // The race the component could not test: the first request is still in
    // flight when the query changes, and its answer must not land.
    const h = harness([
      () => ({ rows: [row('old-1'), row('old-2')], total: 99 }),
      () => ({ rows: [row('new-1')], total: 1 }),
    ]);
    const slow = h.ps.load();
    h.ps.setQuery('new');
    const fast = h.ps.load();
    h.flush();
    await Promise.all([slow, fast]);

    const snap = h.ps.snapshot();
    expect(snap.rows.map((r) => r.id)).toEqual(['new-1']);
    expect(snap.total).toBe(1);
  });

  it('invalidates every cursor when the query changes', async () => {
    const h = harness([() => ({ rows: [row('a')], total: 10, nextCursor: 'CUR' })]);
    const p = h.ps.load();
    h.flush();
    await p;

    h.ps.setPage(1);
    expect(h.ps.state().cursor).toBe('CUR');

    // A cursor names rows in the OLD result set, so a new search cannot reuse
    // it — nor the old total, or the pager offers pages this search may lack.
    h.ps.setQuery('something else');
    expect(h.ps.state().cursor).toBe('');
    expect(h.ps.state().page).toBe(0);
    expect(h.ps.snapshot().total).toBe(0);
  });

  it('reports pageCount from the whole-table total, not the page', async () => {
    const h = harness([() => ({ rows: [row('a'), row('b')], total: 7 })]);
    const p = h.ps.load();
    h.flush();
    await p;
    expect(h.ps.snapshot().pageCount).toBe(4); // ceil(7 / 2)
  });

  it('never reports fewer than one page', () => {
    const h = harness([() => ({ rows: [], total: 0 })]);
    expect(h.ps.snapshot().pageCount).toBe(1);
  });

  it('setQuery and setPage report whether anything changed', () => {
    const h = harness([() => ({ rows: [], total: 0 })]);
    expect(h.ps.setQuery('a')).toBe(true);
    expect(h.ps.setQuery('a')).toBe(false);
    expect(h.ps.setPage(2)).toBe(true);
    expect(h.ps.setPage(2)).toBe(false);
  });
});
