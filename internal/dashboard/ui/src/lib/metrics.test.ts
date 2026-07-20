import { describe, expect, it } from 'vitest';
import { metricFacets, modelSlots, scatterClass, scatterPos, scatterTicksX, scatterTicksY, scatterTipStyle, trendPoints, verdictRing } from './metrics';

const scatter = (...models: string[]) => models.map((model) => ({ model }) as any);

describe('metrics presentation', () => {
  it('derives current-range facets', () => {
    expect(metricFacets({ models: [{ model: 'gpt-5.6-terra', effort: 'high' }, { model: 'gpt-5.5', effort: 'high' }] } as any)).toEqual({ models: ['gpt-5.5', 'gpt-5.6-terra'], efforts: ['high'] });
  });
  it('maps every primary verdict to its scatter colour', () => {
    const slots = new Map<string, number>();
    expect(scatterClass({ verdict: 'APPROVED', model: 'x' } as any, 'verdict', slots)).toBe('approved');
    expect(scatterClass({ verdict: 'COMMENTED', model: 'x' } as any, 'verdict', slots)).toBe('commented');
    expect(scatterClass({ verdict: 'REQUESTED_CHANGES', model: 'x' } as any, 'verdict', slots)).toBe('rejected');
  });
  it('renders non-primary verdicts (SKIPPED/ERROR) as the neutral "other" colour', () => {
    const slots = new Map<string, number>();
    expect(scatterClass({ verdict: 'SKIPPED', model: 'x' } as any, 'verdict', slots)).toBe('other');
    expect(scatterClass({ verdict: 'ERROR', model: 'x' } as any, 'verdict', slots)).toBe('other');
  });
  it('colours by model from the slot map, defaulting unknown models to slot 0', () => {
    const slots = modelSlots(scatter('a', 'b'));
    expect(scatterClass({ verdict: 'APPROVED', model: 'b' } as any, 'model', slots)).toBe('model-1');
    expect(scatterClass({ verdict: 'APPROVED', model: 'missing' } as any, 'model', slots)).toBe('model-0');
  });
});

describe('modelSlots', () => {
  it('gives the first four distinct models distinct slots, sorted for stability', () => {
    // Regression: char-code hashing sent all three of these to slot 0.
    const slots = modelSlots(scatter('gpt-5.5', '', 'gpt-5.6-terra', 'gpt-5.5'));
    expect(slots).toEqual(new Map([['', 0], ['gpt-5.5', 1], ['gpt-5.6-terra', 2]]));
  });
  it('wraps back onto the four palette slots past four models', () => {
    expect(modelSlots(scatter('a', 'b', 'c', 'd', 'e')).get('e')).toBe(0);
  });
});

describe('scatterPos', () => {
  it('insets dots from the axes, scaling to the plot maxima', () => {
    expect(scatterPos({ tokens_used: 100, duration_secs: 100 } as any, 100, 100)).toEqual({ x: 96, y: 90 });
    expect(scatterPos({ tokens_used: 50, duration_secs: 100 } as any, 100, 100)).toEqual({ x: 96, y: 49 });
    expect(scatterPos({ tokens_used: 0, duration_secs: 0 } as any, 100, 100)).toEqual({ x: 8, y: 8 });
  });
});

describe('scatter ticks', () => {
  it('lands round 1/2/5-stepped token values on the dot scale', () => {
    expect(scatterTicksY(100)).toEqual([{ value: 50, pct: 49 }, { value: 100, pct: 90 }]);
    expect(scatterTicksY(1_700_000).map((t) => t.value)).toEqual([500_000, 1_000_000, 1_500_000]);
  });
  it('steps durations along the clock ladder so labels read as round times', () => {
    expect(scatterTicksX(5400).map((t) => t.value)).toEqual([1800, 3600, 5400]);
    expect(scatterTicksX(2000).map((t) => t.value)).toEqual([600, 1200, 1800]);
    expect(scatterTicksX(400_000).map((t) => t.value)).toEqual([172_800, 345_600]);
  });
});

describe('scatterTipStyle', () => {
  it('centres above the dot by default', () => {
    expect(scatterTipStyle(50, 30)).toBe('left:50%; bottom:calc(30% + 12px); transform:translate(-50%, 0)');
  });
  it('flips below near the top and nudges inward near the sides', () => {
    expect(scatterTipStyle(80, 80)).toBe('left:80%; bottom:calc(80% - 14px); transform:translate(-85%, 100%)');
    expect(scatterTipStyle(10, 30)).toContain('translate(-15%, 0)');
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
