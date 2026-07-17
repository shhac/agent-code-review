import { afterEach, describe, expect, it, vi } from 'vitest';
import { del, fetchJSON, post } from './api';

const mockFetch = (res: Response) => {
  const fn = vi.fn<typeof fetch>(async () => res);
  vi.stubGlobal('fetch', fn);
  return fn;
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('fetchJSON', () => {
  it('returns the parsed body on success', async () => {
    mockFetch(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    await expect(fetchJSON('/api/x')).resolves.toEqual({ ok: true });
  });

  it('surfaces the {error} envelope on failure', async () => {
    mockFetch(new Response(JSON.stringify({ error: 'nope' }), { status: 400, statusText: 'Bad Request' }));
    await expect(fetchJSON('/api/x')).rejects.toThrow('nope');
  });

  it('falls back to status text when the error body is not JSON', async () => {
    // A proxy 502 answers with an HTML page; the real failure must not be
    // masked by a JSON parse error.
    mockFetch(new Response('<html>upstream sad</html>', { status: 502, statusText: 'Bad Gateway' }));
    await expect(fetchJSON('/api/x')).rejects.toThrow('Bad Gateway');
  });
});

describe('post/del', () => {
  it('sends a JSON body with the right method', async () => {
    const fn = mockFetch(new Response('{}', { status: 200 }));
    await post('/api/queue', { url: 'o/r/pull/1' });
    await del('/api/queue', { repo: 'o/r', number: 1 });
    expect(fn).toHaveBeenNthCalledWith(1, '/api/queue', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url: 'o/r/pull/1' }),
    });
    expect(fn.mock.calls[1]?.[1]).toMatchObject({ method: 'DELETE' });
  });

  it('unwraps the envelope and survives non-JSON error bodies', async () => {
    mockFetch(new Response(JSON.stringify({ error: 'not a watched repo' }), { status: 403 }));
    await expect(post('/api/queue', {})).rejects.toThrow('not a watched repo');
    mockFetch(new Response('gateway timeout', { status: 504, statusText: 'Gateway Timeout' }));
    await expect(del('/api/queue', {})).rejects.toThrow('Gateway Timeout');
  });
});
