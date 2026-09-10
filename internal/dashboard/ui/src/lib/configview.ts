// The Config page's read-only derivations: what the daemon is running, phrased
// for a person. Pure, so the disabled-vs-enabled wordings are table-testable
// rather than only reachable by rendering the page — which is how the rest of
// this project's route logic is already organised (lib/metrics.ts, poll.ts).

import type { ConfigResponse } from './types';

export type SettingsGroup = [string, [string, string][]];

// One wording for the per-daemon loop state cells (review + discovery).
// Three states, not two: a loop can be enabled in config and still off because
// this daemon booted with the flag disabled, and conflating that with "off"
// hides why nothing is happening.
export function loopState(running: boolean, enabled: boolean): string {
  if (running) return 'running';
  if (enabled) return 'off (config enabled, boot flag disabled)';
  return 'off';
}

export function settingsGroups(c: ConfigResponse | null): SettingsGroup[] {
  if (!c) return [];
  return [

    ['Daemon', [
      ['Version', c.version || 'dev'],
      ['Reviewing as', c.reviewing_as ? `@${c.reviewing_as}` : 'unknown (gh not authenticated?)'],
    ]],
    ['Review loop', [
      ['State (this daemon)', loopState(c.review_running, c.schedule.enabled)],
      ['Default engine', c.engine],
      [`${c.engine} model`, c.engine_config.model || 'engine default'],
      [`${c.engine} effort`, c.engine_config.effort || 'model default'],
      ['Interval', c.schedule.interval],
      ['Max parallel', String(c.schedule.max_parallel)],
      ['Usage floor (5h)', c.schedule.usage_floor_5h_percent ? `hold below ${c.schedule.usage_floor_5h_percent}% remaining, per engine` : 'disabled'],
      ['Usage floor (weekly)', c.schedule.usage_floor_weekly_percent ? `hold below ${c.schedule.usage_floor_weekly_percent}% remaining, per engine` : 'disabled'],
    ]],
    ['Discovery', [
      ['State (this daemon)', loopState(c.discovery_running, c.discovery.enabled)],
      ['Interval', c.discovery.interval],
    ]],
    ['Candidate eligibility', [
      ['New PR window', `${c.candidates.new_max_age_days} days`],
      ['Refreshed window', `${c.candidates.refreshed_max_age_days} days`],
      ['Discussion window', `${c.candidates.discussion_max_age_days} days`],
      ['Re-review cooldown', c.candidates.rereview_cooldown === '0s' ? 'disabled' : `hold ${c.candidates.rereview_cooldown} after our review`],
      ['Quiet period', c.candidates.quiet_period === '0s' ? 'disabled' : `hold until untouched for ${c.candidates.quiet_period}`],
    ]],
  
  ];
}
