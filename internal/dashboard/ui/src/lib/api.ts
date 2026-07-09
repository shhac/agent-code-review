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
  StatsResponse,
  UsageResponse,
  ReviewLogRef,
} from './types';

export async function fetchJSON<T = any>(path: string): Promise<T> {
  const res = await fetch(path);
  const data = await res.json();
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data as T;
}

export async function post(path: string, body: unknown) {
  const res = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error((await res.json()).error || res.statusText);
}

export async function del(path: string, body: unknown) {
  const res = await fetch(path, {
    method: 'DELETE',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error((await res.json()).error || res.statusText);
}

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
