import { describe, expect, it } from 'vitest';
import { metricFacets, modelColourIndex, scatterClass, scatterPos, trendPoints, verdictRing } from './metrics';

describe('metrics presentation', () => {
  it('derives current-range facets', () => {
    expect(metricFacets({ models: [{ model: 'gpt-5.6-terra', effort: 'high' }, { model: 'gpt-5.5', effort: 'high' }] } as any)).toEqual({ models: ['gpt-5.5', 'gpt-5.6-terra'], efforts: ['high'] });
  });
  it('maps every primary verdict to its scatter colour', () => {
    expect(scatterClass({ verdict: 'APPROVED', model: 'x' } as any, 'verdict')).toBe('approved');
    expect(scatterClass({ verdict: 'COMMENTED', model: 'x' } as any, 'verdict')).toBe('commented');
    expect(scatterClass({ verdict: 'REQUESTED_CHANGES', model: 'x' } as any, 'verdict')).toBe('rejected');
  });
  it('renders non-primary verdicts (SKIPPED/ERROR) as the neutral "other" colour', () => {
    expect(scatterClass({ verdict: 'SKIPPED', model: 'x' } as any, 'verdict')).toBe('other');
    expect(scatterClass({ verdict: 'ERROR', model: 'x' } as any, 'verdict')).toBe('other');
  });
  it('colours by model with a stable palette slot', () => {
    expect(scatterClass({ verdict: 'APPROVED', model: 'x' } as any, 'model')).toBe(`model-${modelColourIndex('x')}`);
  });
});

describe('modelColourIndex', () => {
  it('is stable and stays within the four-slot palette', () => {
    expect(modelColourIndex('x')).toBe('x'.charCodeAt(0) % 4);
    expect(modelColourIndex('gpt-5.6-terra')).toBe(modelColourIndex('gpt-5.6-terra'));
    expect(modelColourIndex('gpt-5.6-terra')).toBeGreaterThanOrEqual(0);
    expect(modelColourIndex('gpt-5.6-terra')).toBeLessThan(4);
  });
});

describe('scatterPos', () => {
  it('insets dots from the axes, scaling to the plot maxima', () => {
    expect(scatterPos({ tokens_used: 100, duration_secs: 100 } as any, 100, 100)).toEqual({ left: '96%', bottom: '90%' });
    expect(scatterPos({ tokens_used: 0, duration_secs: 0 } as any, 100, 100)).toEqual({ left: '8%', bottom: '8%' });
  });
});

describe('verdictRing', () => {
  it('builds cumulative wedges per primary verdict', () => {
    expect(verdictRing({ APPROVED: 1, COMMENTED: 1 })).toBe(
      'conic-gradient(var(--green) 0 180deg, var(--blue) 0 360deg, var(--red) 0 360deg, var(--amber) 0)',
    );
  });
  it('leaves the amber wedge for non-primary verdicts (e.g. SKIPPED)', () => {
    expect(verdictRing({ APPROVED: 2, SKIPPED: 2 })).toBe(
      'conic-gradient(var(--green) 0 180deg, var(--blue) 0 180deg, var(--red) 0 180deg, var(--amber) 0)',
    );
  });
  it('degrades to empty wedges when there are no verdicts', () => {
    expect(verdictRing({})).toBe('conic-gradient(var(--green) 0 0deg, var(--blue) 0 0deg, var(--red) 0 0deg, var(--amber) 0)');
  });
});

describe('trendPoints', () => {
  it('centres a single day since it has no span', () => {
    expect(trendPoints([{ day: 'a', reviews: 0, tokens_used: 50 }], 100)).toBe('50,50');
  });
  it('spreads days across the viewBox with an inverted y axis', () => {
    expect(trendPoints([{ day: 'a', reviews: 0, tokens_used: 0 }, { day: 'b', reviews: 0, tokens_used: 100 }], 100)).toBe('0,100 100,0');
  });
});
