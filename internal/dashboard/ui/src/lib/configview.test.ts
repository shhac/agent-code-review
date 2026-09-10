import { describe, expect, it } from 'vitest';
import { loopState, settingsGroups } from './configview';
import type { ConfigResponse } from './types';

const cfg = (over: Partial<ConfigResponse> = {}) =>
  ({
    version: 'dev', reviewing_as: 'paul-gh', engine: 'codex',
    engine_config: { model: '', effort: '' },
    review_running: true, discovery_running: true,
    schedule: { enabled: true, interval: '30s', max_parallel: 4, usage_floor_5h_percent: 0, usage_floor_weekly_percent: 10 },
    discovery: { enabled: true, interval: '5m' },
    candidates: {
      new_max_age_days: 14, refreshed_max_age_days: 21, discussion_max_age_days: 14,
      rereview_cooldown: '90m', quiet_period: '15m',
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
    expect(c['Version']).toBe('dev');
    expect(c['Reviewing as']).toContain('gh not authenticated');
  });
});
