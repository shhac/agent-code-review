// The Config page's read-only derivations: what the daemon is running, phrased
// for a person. Pure, so the disabled-vs-enabled wordings are table-testable
// rather than only reachable by rendering the page — which is how the rest of
// this project's route logic is already organised (lib/metrics.ts, poll.ts).

import type { ConfigResponse, ScoringMode } from './types';

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

// scoringRows describes what a review earns its author.
//
// The mode comes first because it is the only row that can make every other
// one moot. The multipliers are shown as a rate per unit rather than as raw
// numbers: "1.5x per 50 lines" answers the question people actually arrive
// with, which is why their PR scored what it did.
function scoringRows(c: ConfigResponse): [string, string][] {
  const s = c.scoring;
  const mode: Record<ScoringMode, string> = {
    enabled: 'scoring reviews',
    'leaderboard-only': 'paused (standings still shown)',
    disabled: 'off (leaderboard hidden)',
  };
  if (s.mode !== 'enabled') return [['Mode', mode[s.mode] ?? s.mode]];
  return [
    ['Mode', mode[s.mode]],
    ['Base', `${s.base} points per ${s.churn_unit} lines`],
    ['Deletion weight', `a removed line counts ${s.deletion_weight}x an added one`],
    ['Verdicts', `approve ${s.approved}x · comment ${s.commented}x · changes ${s.requested_changes}x`],
    ['Shrink bonus', `${s.shrink_bonus}x when a PR is net-negative`],
    ['Revision decay', `${s.attempt_decay}x per extra round of review`],
    ['Generated files', s.use_gitattributes
      ? `excluded via the repo's .gitattributes${s.exclude_paths ? `, plus ${s.exclude_paths} glob(s)` : ''}`
      : s.exclude_paths ? `${s.exclude_paths} glob(s) only` : 'all files counted'],
  ];
}

export function settingsGroups(c: ConfigResponse | null): SettingsGroup[] {
  if (!c) return [];
  return [

    ['Daemon', [
      ['Version', c.version || 'dev'],
      ['Reviewing as', c.reviewing_as ? `@${c.reviewing_as}` : 'unknown (gh not authenticated?)'],
      ['Transcript retention', c.workspace_retention === '0s' ? 'kept forever' : `swept after ${c.workspace_retention}`],
    ]],
    ['Review loop', [
      ['State (this daemon)', loopState(c.review_running, c.schedule.enabled)],
      ['Default engine', c.engine],
      [`${c.engine} model`, c.engine_config.model || 'engine default'],
      [`${c.engine} effort`, c.engine_config.effort || 'model default'],
      ['Interval', c.schedule.interval],
      ['Max parallel', String(c.schedule.max_parallel)],
      ['Dispatch cooldown', c.schedule.dispatch_cooldown === '0s' ? 'none' : `${c.schedule.dispatch_cooldown} between hand-offs`],
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
      ['Steering hold', c.candidates.steering_hold === '0s' ? 'disabled' : `hold ${c.candidates.steering_hold} while an editor is open`],
      ['Error backoff', c.candidates.error_backoff === '0s' ? 'retire on first error' : `retry once after ${c.candidates.error_backoff}`],
    ]],
    ['Scoring', scoringRows(c)],
  
  ];
}
