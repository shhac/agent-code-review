import { describe, expect, it } from 'vitest';
import { metricFacets, scatterClass } from './metrics';

describe('metrics presentation', () => {
  it('derives current-range facets', () => {
    expect(metricFacets({ models: [{ model: 'gpt-5.6-terra', effort: 'high' }, { model: 'gpt-5.5', effort: 'high' }] } as any)).toEqual({ models: ['gpt-5.5', 'gpt-5.6-terra'], efforts: ['high'] });
  });
  it('maps verdict and model scatter colours', () => {
    expect(scatterClass({ verdict: 'REQUESTED_CHANGES', model: 'x' } as any, 'verdict')).toBe('rejected');
    expect(scatterClass({ verdict: 'APPROVED', model: 'x' } as any, 'model')).toMatch(/^model-/);
  });
});
