import { describe, expect, it } from 'vitest';
import { codeSpans, loopState, scoringDialsShown, settingsGroups } from './configview';
import type { ConfigResponse } from './types';

const cfg = (over: Partial<ConfigResponse> = {}) =>
  ({
    version: 'dev', reviewing_as: 'paul-gh', engine: 'codex',
    engine_config: { model: '', effort: '' },
    review_running: true, discovery_running: true,
    schedule: { enabled: true, interval: '30s', max_parallel: 4, dispatch_cooldown: '5s', usage_floor_5h_percent: 0, usage_floor_weekly_percent: 10 },
    discovery: { enabled: true, interval: '5m' },
    candidates: {
      new_max_age_days: 14, refreshed_max_age_days: 21, discussion_max_age_days: 14,
      rereview_cooldown: '90m', quiet_period: '15m',
      steering_hold: '5m', error_backoff: '15m',
    },
    workspace_retention: '720h0m0s',
    scoring: {
      mode: 'enabled', leaderboard_visible: true,
      base: 100, churn_unit: 50, deletion_weight: 0.5,
      approved: 1, commented: 0.25, requested_changes: -0.25,
      shrink_bonus: 1.2, attempt_decay: 0.6,
      curve: 'linear', use_gitattributes: true, exclude_paths: 0,
      buckets: [
        { name: 'tiny', max_churn: 10, multiplier: 1 },
        { name: 'small', max_churn: 50, multiplier: 1.5 },
        { name: 'huge', max_churn: 0, multiplier: 0.2 },
      ],
    },
    ...over,
  }) as ConfigResponse;

const cells = (g: ReturnType<typeof settingsGroups>) =>
  Object.fromEntries(g.flatMap(([, rows]) => rows));

describe('loopState', () => {
  it('separates "off" from "off because this daemon booted without it"', () => {
    // Three states, not two: conflating them hides why nothing is happening.
    expect(loopState(true, true)).toBe('running');
    expect(loopState(false, false)).toBe('off');
    expect(loopState(false, true)).toContain('boot flag disabled');
  });
});

describe('settingsGroups', () => {
  it('has nothing to say before the config arrives', () => {
    expect(settingsGroups(null)).toEqual([]);
  });

  it('says "disabled" for a 0s hold rather than showing 0s', () => {
    // The dials use 0s to mean off, which is true but not what a reader wants
    // to see in a settings table.
    const c = cells(settingsGroups(cfg({
      candidates: { ...cfg().candidates, rereview_cooldown: '0s', quiet_period: '0s' },
    })));
    expect(c['Re-review cooldown']).toBe('disabled');
    expect(c['Quiet period']).toBe('disabled');
  });

  it('spells out a live hold', () => {
    const c = cells(settingsGroups(cfg()));
    expect(c['Re-review cooldown']).toContain('90m');
    expect(c['Quiet period']).toContain('15m');
  });

  it('distinguishes a disabled usage floor from a set one', () => {
    // Money gate: 0 means the floor is off, and reading it as "hold below 0%"
    // would suggest a protection that is not there.
    const c = cells(settingsGroups(cfg()));
    expect(c['Usage floor (5h)']).toBe('disabled');
    expect(c['Usage floor (weekly)']).toContain('10%');
  });

  it('falls back where the daemon could not answer', () => {
    const c = cells(settingsGroups(cfg({ version: '', reviewing_as: '' })));
    expect(c['Version']).toBe('`dev`');
    expect(c['Reviewing as']).toContain('gh not authenticated');
  });
});

describe('scoring cluster', () => {
  it('states the dials as a rate, which is the question people arrive with', () => {
    const c = cells(settingsGroups(cfg()));
    // The per-line rate, not base-per-unit: the latter invites the reader to
    // expect 50 lines to score 100, when 50 lines lands in a 1.5x tier.
    expect(c['Base rate']).toBe('`2` points per line of churn, before the size multiplier');
    expect(c['Size tiers']).toContain('small \u226450: `1.5x`');
    expect(c['Size tiers']).toContain('huge: `0.2x`');
    expect(c['Deletion weight']).toContain('`0.5x` an added one');
    expect(c['Verdicts']).toContain('approve `1x`');
    expect(c['Verdicts']).toContain('changes `-0.25x`');
    expect(c['Revision decay']).toContain('`0.6x`');
  });

  // When scoring is off, every other dial is moot: showing eight rows that do
  // not apply would be worse than showing one that explains why.
  it('collapses to a single row when scoring is not running', () => {
    for (const mode of ['leaderboard-only', 'disabled'] as const) {
      const groups = settingsGroups(cfg({ scoring: { ...cfg().scoring, mode } }));
      const scoring = groups.find(([name]) => name === 'Scoring');
      expect(scoring?.[1]).toHaveLength(1);
      expect(scoring?.[1][0][0]).toBe('Mode');
    }
    expect(cells(settingsGroups(cfg({ scoring: { ...cfg().scoring, mode: 'disabled' } })))['Mode'])
      .toContain('leaderboard hidden');
  });

  it('says how generated files are decided', () => {
    expect(cells(settingsGroups(cfg()))['Generated files']).toContain('.gitattributes');

    const noAttrs = cfg({ scoring: { ...cfg().scoring, use_gitattributes: false, exclude_paths: 0 } });
    expect(cells(settingsGroups(noAttrs))['Generated files']).toBe('all files counted');

    const globs = cfg({ scoring: { ...cfg().scoring, use_gitattributes: false, exclude_paths: 3 } });
    expect(cells(settingsGroups(globs))['Generated files']).toBe('3 glob(s) only');
  });
});

describe('completed clusters', () => {
  it('shows the holds that were previously invisible', () => {
    const c = cells(settingsGroups(cfg()));
    expect(c['Steering hold']).toContain('5m');
    expect(c['Error backoff']).toContain('15m');
    expect(c['Dispatch cooldown']).toContain('5s');
    expect(c['Transcript retention']).toContain('720h');
  });

  it('spells out the off states rather than printing 0s', () => {
    const c = cells(settingsGroups(cfg({
      candidates: { ...cfg().candidates, steering_hold: '0s', error_backoff: '0s' },
      workspace_retention: '0s',
    })));
    expect(c['Steering hold']).toBe('disabled');
    expect(c['Error backoff']).toBe('retire on first error');
    expect(c['Transcript retention']).toBe('kept forever');
  });
});

describe('codeSpans', () => {
  it('separates the machine value from the sentence around it', () => {
    expect(codeSpans('hold `1h30m0s` after our review')).toEqual([
      { text: 'hold ', code: false },
      { text: '1h30m0s', code: true },
      { text: ' after our review', code: false },
    ]);
  });

  it('handles several values in one row', () => {
    const spans = codeSpans('approve `1x` · comment `0.25x`');
    expect(spans.filter((s) => s.code).map((s) => s.text)).toEqual(['1x', '0.25x']);
  });

  it('leaves a plain sentence alone', () => {
    expect(codeSpans('kept forever')).toEqual([{ text: 'kept forever', code: false }]);
  });

  // A value that fills the whole row must not render an empty prose span
  // either side of itself.
  it('drops the empty edges around a bare value', () => {
    expect(codeSpans('`codex`')).toEqual([{ text: 'codex', code: true }]);
  });

  it('has nothing to render for an empty value', () => {
    expect(codeSpans('')).toEqual([]);
  });
});

describe('settings values carry their machine parts as code', () => {
  it('marks durations, counts and multipliers', () => {
    const c = cells(settingsGroups(cfg()));
    const coded = (row: string) => codeSpans(c[row]).filter((s) => s.code).map((s) => s.text);
    expect(coded('Re-review cooldown')).toEqual(['90m']);
    expect(coded('Max parallel')).toEqual(['4']);
    expect(coded('Verdicts')).toEqual(['1x', '0.25x', '-0.25x']);
    expect(coded('Base rate')).toEqual(['2']);
  });

  // "disabled" and "kept forever" are prose, not values: wrapping them would
  // dress an explanation up as a setting.
  it('leaves the off-states as plain prose', () => {
    const c = cells(settingsGroups(cfg({
      candidates: { ...cfg().candidates, rereview_cooldown: '0s' },
      workspace_retention: '0s',
    })));
    expect(codeSpans(c['Re-review cooldown']).some((s) => s.code)).toBe(false);
    expect(codeSpans(c['Transcript retention']).some((s) => s.code)).toBe(false);
  });
});

describe('scoringDialsShown owns when there is a policy to explain', () => {
  // The settings table and the tier chart both ask this, so it lives in one
  // place: a page that showed the curve beside "scoring is off" would be
  // explaining a policy nothing is applying.
  it('is true only while reviews are actually being scored', () => {
    expect(scoringDialsShown(cfg())).toBe(true);
    for (const mode of ['leaderboard-only', 'disabled'] as const) {
      expect(scoringDialsShown(cfg({ scoring: { ...cfg().scoring, mode } }))).toBe(false);
    }
    expect(scoringDialsShown(null)).toBe(false);
  });

  it('is the same rule the settings table uses', () => {
    const c = cells(settingsGroups(cfg({ scoring: { ...cfg().scoring, mode: 'disabled' } })));
    expect(Object.keys(c)).not.toContain('Curve');
  });
});

describe('the curve is named, because the ladder alone does not say', () => {
  // The same five numbers mean two different things: rates at a boundary
  // under "linear", flat rates across a tier under "step". A reader given
  // only the ladder would mis-predict every PR not sitting on a boundary.
  it('says the rate ramps under the shipped linear curve', () => {
    expect(cells(settingsGroups(cfg()))['Curve']).toContain('ramps');
  });

  it('says every boundary is a cliff under step', () => {
    const c = cells(settingsGroups(cfg({ scoring: { ...cfg().scoring, curve: 'step' } })));
    expect(c['Curve']).toContain('cliff');
  });

  // The ladder row stays the plain list of configured figures either way.
  it('keeps the ladder free of curve wording', () => {
    for (const curve of ['linear', 'step'] as const) {
      const c = cells(settingsGroups(cfg({ scoring: { ...cfg().scoring, curve } })));
      expect(c['Size tiers']).toBe('tiny \u226410: `1x` \u00b7 small \u226450: `1.5x` \u00b7 huge: `0.2x`');
    }
  });
});
