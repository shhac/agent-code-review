// Fetch layer: JSON in/out with the API's {error} envelope surfaced as throws.

import type {
  AuthorsResponse,
  ConfigResponse,
  QueueResponse,
  ReviewLogResponse,
  ReviewsResponse,
  LogsResponse,
  MetricsResponse,
  PromptResponse,
  PromptPreviewResponse,
  StatsResponse,
  Viewer,
  QueueAdd,
  QueuePreflight,
  UsageResponse,
  ReviewLogRef,
} from './types';

// errorFrom unwraps the API's {error} envelope from a failed response. The
// body may not be JSON at all (a proxy 502's HTML page, an empty reply), so
// a parse failure falls back to the status text instead of masking the real
// failure with a SyntaxError.
async function errorFrom(res: Response): Promise<Error> {
  try {
    const data = await res.json();
    return new Error(data.error || res.statusText);
  } catch {
    return new Error(res.statusText || `HTTP ${res.status}`);
  }
}

export async function fetchJSON<T = any>(path: string): Promise<T> {
  const res = await fetch(path);
  if (!res.ok) throw await errorFrom(res);
  return (await res.json()) as T;
}

// send is the one write-path frame: JSON body out, {error} envelope on
// failure. post/del are thin partial applications.
async function send(method: 'POST' | 'DELETE', path: string, body: unknown) {
  const res = await fetch(path, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw await errorFrom(res);
}

export const post = (path: string, body: unknown) => send('POST', path, body);

// postJSON is send for the few writes whose RESPONSE matters.
export async function postJSON<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw await errorFrom(res);
  return (await res.json()) as T;
}
export const del = (path: string, body: unknown) => send('DELETE', path, body);

type PRRef = { repo: string; number: number };

export const getQueue = () => fetchJSON<QueueResponse>('/api/queue');
export const getReviews = (opts: { q?: string; limit?: number; cursor?: string } = {}) => {
  const params = new URLSearchParams({ limit: String(opts.limit ?? 50) });
  if (opts.q) params.set('q', opts.q);
  if (opts.cursor) params.set('cursor', opts.cursor);
  return fetchJSON<ReviewsResponse>(`/api/reviews?${params}`);
};
export const getUsage = () => fetchJSON<UsageResponse>('/api/usage');
export const getStats = () => fetchJSON<StatsResponse>('/api/stats');
export const getMetrics = (range: string, model: string, effort: string) => {
  const params = new URLSearchParams({ range });
  if (model) params.set('model', model);
  if (effort) params.set('effort', effort);
  return fetchJSON<MetricsResponse>(`/api/metrics?${params}`);
};
export const getConfig = () => fetchJSON<ConfigResponse>('/api/config');
export const getAuthors = () => fetchJSON<AuthorsResponse>('/api/authors');
export const getPrompt = () => fetchJSON<PromptResponse>('/api/prompt');
export const getPromptPreview = (p: {
  author_is_gh_user: boolean;
  candidate_type: string;
  repo?: string;
  group?: string;
  author?: string;
}) => {
  const params = new URLSearchParams({
    author_is_gh_user: String(p.author_is_gh_user),
    candidate_type: p.candidate_type,
  });
  if (p.repo) params.set('repo', p.repo);
  // A named group is simulated; a named author additionally fires their own
  // overrides, which is the only way to preview one.
  if (p.group) params.set('group', p.group);
  if (p.author) params.set('author', p.author);
  return fetchJSON<PromptPreviewResponse>(`/api/prompt/preview?${params.toString()}`);
};
export const getLogs = () => fetchJSON<LogsResponse>('/api/logs');

export function getReviewLog(ref: ReviewLogRef) {
  let url = `/api/review-log?repo=${encodeURIComponent(ref.repo)}&number=${ref.number}`;
  if (ref.logKey) url += `&review=${encodeURIComponent(ref.logKey)}`;
  return fetchJSON<ReviewLogResponse>(url);
}

// postJSON, not post: the response says whether an accompanying steering
// message was applied, and dropping it would let a refusal pass as success.
export const queuePR = (url: string, steering = '') =>
  postJSON<QueueAdd>('/api/queue', { url, steering });

// preflightPR resolves a PR reference without queueing it, so the add form can
// ask who wrote it and whether this viewer may steer it. Advisory: the add
// re-resolves and re-checks, so nothing here is load-bearing.
export const preflightPR = (url: string) => postJSON<QueuePreflight>('/api/queue/preflight', { url });
export const removeQueuedPR = ({ repo, number }: PRRef) => del('/api/queue', { repo, number });
export const promoteQueuedPR = ({ repo, number }: PRRef) => post('/api/queue/promote', { repo, number });
export const reorderQueue = (order: PRRef[]) => post('/api/queue/reorder', { order });

export const getViewer = () => fetchJSON<Viewer>('/api/viewer');

// setSteering sets the instruction for one PR, or clears it when message is
// empty. The server decides whether the caller may: it reads the PR's author
// from the queue, never from this request, so a rejection here is the answer.
export const setSteering = (repo: string, number: number, message: string) =>
  post('/api/steering', { repo, number, message });
