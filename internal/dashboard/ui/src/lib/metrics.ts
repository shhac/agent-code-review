import type { MetricsResponse } from './types';

export type ColourMode = 'verdict' | 'model';

export function metricFacets(data: MetricsResponse | null) {
  const rows = data?.models || [];
  return {
    models: [...new Set(rows.map((m) => m.model).filter(Boolean))].sort(),
    efforts: [...new Set(rows.map((m) => m.effort).filter(Boolean))].sort(),
  };
}

export function scatterClass(point: MetricsResponse['scatter'][number], mode: ColourMode) {
  if (mode === 'model') return `model-${[...point.model].reduce((n, c) => n + c.charCodeAt(0), 0) % 4}`;
  return ({ APPROVED: 'approved', COMMENTED: 'commented', REQUESTED_CHANGES: 'rejected' } as Record<string, string>)[point.verdict] || 'other';
}
