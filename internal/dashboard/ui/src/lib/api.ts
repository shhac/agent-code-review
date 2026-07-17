// Fetch layer: JSON in/out with the API's {error} envelope surfaced as throws.

import type {
  AuthorsResponse,
  ConfigResponse,
  QueueResponse,
  ReviewLogResponse,
  ReviewsResponse,
  RunsResponse,
  LogsResponse,
  MetricsResponse,
  PromptResponse,
  PromptPreviewResponse,
  StatsResponse,
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
export const del = (path: string, body: unknown) => send('DELETE', path, body);

type PRRef = { repo: string; number: number };

export const getQueue = () => fetchJSON<QueueResponse>('/api/queue');
export const getReviews = (limit = 100) => fetchJSON<ReviewsResponse>(`/api/reviews?limit=${limit}`);
export const getRuns = (limit = 100) => fetchJSON<RunsResponse>(`/api/runs?limit=${limit}`);
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
  author_allowed: boolean;
  author_is_gh_user: boolean;
  candidate_type: string;
  repo?: string;
}) => {
  const params = new URLSearchParams({
    author_allowed: String(p.author_allowed),
    author_is_gh_user: String(p.author_is_gh_user),
    candidate_type: p.candidate_type,
  });
  if (p.repo) params.set('repo', p.repo);
  return fetchJSON<PromptPreviewResponse>(`/api/prompt/preview?${params.toString()}`);
};
export const getLogs = () => fetchJSON<LogsResponse>('/api/logs');

export function getReviewLog(ref: ReviewLogRef) {
  let url = `/api/review-log?repo=${encodeURIComponent(ref.repo)}&number=${ref.number}`;
  if (ref.logKey) url += `&review=${encodeURIComponent(ref.logKey)}`;
  return fetchJSON<ReviewLogResponse>(url);
}

export const queuePR = (url: string) => post('/api/queue', { url });
export const removeQueuedPR = ({ repo, number }: PRRef) => del('/api/queue', { repo, number });
export const promoteQueuedPR = ({ repo, number }: PRRef) => post('/api/queue/promote', { repo, number });
export const reorderQueue = (order: PRRef[]) => post('/api/queue/reorder', { order });
