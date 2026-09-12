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

// codeSpans splits a settings value into prose and the machine values inside
// it, marked by backticks.
//
// The rows read as sentences ("hold 1h30m0s after our review") because a bare
// duration in a table says nothing about what it does. But the value is the
// part a reader is actually looking for, and set in the same face as the
// sentence it disappears into it.
//
// Deliberately NOT mdToHtml, which this repo already has: that renders a whole
// markdown document, wraps its output in <p>, and has to be trusted through
// {@html}. Here the strings are ours, the only construct is inline code, and
// returning spans lets Svelte escape them as ordinary text.
export function codeSpans(value: string): { text: string; code: boolean }[] {
  return value
    .split('`')
    .map((text, i) => ({ text, code: i % 2 === 1 }))
    .filter((s) => s.text !== '');
}

// round2 trims the float noise a division can leave (100/30 = 3.3333…).
function round2(n: number): string {
  return String(Math.round(n * 100) / 100);
}

// scoringDialsShown reports whether the scoring policy is worth showing at
// all: only a mode that is actually scoring has dials a reader can act on.
//
// Exported because the Config page draws the reward curve next to the settings
// table, and the two must agree about when there is a policy to explain. The
// template used to compare the mode string itself, which was the page's only
// raw mode comparison and a second owner of this rule.
export function scoringDialsShown(c: ConfigResponse | null): boolean {
  return c?.scoring.mode === 'enabled';
}

// scoringRows describes what a review earns its author.
//
// The mode comes first because it is the only row that can make every other
// one moot. The rest are phrased as the questions the dials answer rather than
// as the dials themselves: "the size that earns the most per line" is what
// somebody wants to know, and piece_lines is merely where they would type it.
function scoringRows(c: ConfigResponse): [string, string][] {
  const s = c.scoring;
  const mode: Record<ScoringMode, string> = {
    enabled: 'scoring reviews',
    'leaderboard-only': 'paused (standings still shown)',
    disabled: 'off (leaderboard hidden)',
  };
  if (!scoringDialsShown(c)) return [['Mode', mode[s.mode] ?? s.mode]];
  return [
    ['Mode', mode[s.mode]],
    ['Best piece size', `\`${s.piece_lines}\` changed lines, which earns the most points per line`],
    ['Best single PR', `\`${Math.round(s.peak)}\` changed lines, worth \`${s.size_points}\` points`],
    ['Size falloff', `\`${s.size_falloff}\`, so a PR past the peak is worth less the bigger it gets`],
    ['Removing code', s.removal_points_per_100 > 0
      ? `\`${round2(s.removal_points_per_100 / 100)}\` points a line, net, on top of the size reward`
      : 'earns nothing beyond the size reward'],
    ['Verdicts', `approve \`${s.approved}x\` \u00b7 comment \`${s.commented}x\` \u00b7 changes \`${s.requested_changes}x\``],
    ['Revision decay', `\`${s.attempt_decay}x\` per extra round of review`],
    ['Generated files', s.use_gitattributes
      ? `excluded via the repo's .gitattributes${s.exclude_paths ? `, plus ${s.exclude_paths} glob(s)` : ''}`
      : s.exclude_paths ? `${s.exclude_paths} glob(s) only` : 'all files counted'],
  ];
}

export function settingsGroups(c: ConfigResponse | null): SettingsGroup[] {
  if (!c) return [];
  return [

    ['Daemon', [
      ['Version', `\`${c.version || 'dev'}\``],
      ['Reviewing as', c.reviewing_as ? `@${c.reviewing_as}` : 'unknown (gh not authenticated?)'],
      ['Transcript retention', c.workspace_retention === '0s' ? 'kept forever' : `swept after \`${c.workspace_retention}\``],
    ]],
    ['Review loop', [
      ['State (this daemon)', loopState(c.review_running, c.schedule.enabled)],
      ['Default engine', `\`${c.engine}\``],
      [`${c.engine} model`, c.engine_config.model ? `\`${c.engine_config.model}\`` : 'engine default'],
      [`${c.engine} effort`, c.engine_config.effort ? `\`${c.engine_config.effort}\`` : 'model default'],
      ['Interval', `\`${c.schedule.interval}\``],
      ['Max parallel', `\`${c.schedule.max_parallel}\``],
      ['Dispatch cooldown', c.schedule.dispatch_cooldown === '0s' ? 'none' : `\`${c.schedule.dispatch_cooldown}\` between hand-offs`],
      ['Usage floor (5h)', c.schedule.usage_floor_5h_percent ? `hold below \`${c.schedule.usage_floor_5h_percent}%\` remaining, per engine` : 'disabled'],
      ['Usage floor (weekly)', c.schedule.usage_floor_weekly_percent ? `hold below \`${c.schedule.usage_floor_weekly_percent}%\` remaining, per engine` : 'disabled'],
    ]],
    ['Discovery', [
      ['State (this daemon)', loopState(c.discovery_running, c.discovery.enabled)],
      ['Interval', `\`${c.discovery.interval}\``],
    ]],
    ['Candidate eligibility', [
      ['New PR window', `\`${c.candidates.new_max_age_days}\` days`],
      ['Refreshed window', `\`${c.candidates.refreshed_max_age_days}\` days`],
      ['Discussion window', `\`${c.candidates.discussion_max_age_days}\` days`],
      ['Re-review cooldown', c.candidates.rereview_cooldown === '0s' ? 'disabled' : `hold \`${c.candidates.rereview_cooldown}\` after our review`],
      ['Quiet period', c.candidates.quiet_period === '0s' ? 'disabled' : `hold until untouched for \`${c.candidates.quiet_period}\``],
      ['Steering hold', c.candidates.steering_hold === '0s' ? 'disabled' : `hold \`${c.candidates.steering_hold}\` while an editor is open`],
      ['Error backoff', c.candidates.error_backoff === '0s' ? 'retire on first error' : `retry once after \`${c.candidates.error_backoff}\``],
    ]],
    ['Scoring', scoringRows(c)],
  
  ];
}
