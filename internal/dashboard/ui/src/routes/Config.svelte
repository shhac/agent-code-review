<script lang="ts">
  import { onMount } from 'svelte';
  import { getAuthors, getConfig } from '../lib/api';
  import { withFeed } from '../lib/feed';
  import type { AllowedAuthor, ConfigResponse } from '../lib/types';

  type SettingsGroup = [string, [string, string][]];

  let configData: ConfigResponse | null = null;
  let authors: AllowedAuthor[] = [];
  $: settingsGroups = configData ? ([
    ['Daemon', [
      ['Version', configData.version || 'dev'],
      ['Reviewing as', configData.reviewing_as ? `@${configData.reviewing_as}` : 'unknown (gh not authenticated?)'],
    ]],
    ['Review loop', [
      ['State (this daemon)', configData.review_running ? 'running' : configData.schedule.enabled ? 'off (config enabled, boot flag disabled)' : 'off'],
      ['Engine', configData.engine],
      ['Codex model', configData.codex.model || 'Codex default'],
      ['Codex effort', configData.codex.effort || 'model default'],
      ['Interval', configData.schedule.interval],
      ['Max parallel', String(configData.schedule.max_parallel)],
      ['Usage floor (5h)', configData.schedule.usage_floor_5h_percent ? `pause below ${configData.schedule.usage_floor_5h_percent}% remaining` : 'disabled'],
      ['Usage floor (weekly)', configData.schedule.usage_floor_weekly_percent ? `pause below ${configData.schedule.usage_floor_weekly_percent}% remaining` : 'disabled'],
    ]],
    ['Discovery', [
      ['State (this daemon)', configData.discovery_running ? 'running' : configData.discovery.enabled ? 'off (config enabled, boot flag disabled)' : 'off'],
      ['Interval', configData.discovery.interval],
    ]],
    ['Candidate eligibility', [
      ['New PR window', `${configData.candidates.new_max_age_days} days`],
      ['Refreshed window', `${configData.candidates.refreshed_max_age_days} days`],
      ['Re-review cooldown', configData.candidates.rereview_cooldown === '0s' ? 'disabled' : `hold ${configData.candidates.rereview_cooldown} after our review`],
      ['Quiet period', configData.candidates.quiet_period === '0s' ? 'disabled' : `hold until untouched for ${configData.candidates.quiet_period}`],
    ]],
  ] satisfies SettingsGroup[]) : [];

  async function load() {
    const [cfg, au] = await Promise.all([getConfig(), getAuthors()]);
    configData = cfg;
    authors = au.authors || [];
    return 'read-only';
  }

  onMount(withFeed(load));
</script>

<section class="page-head">
  <p class="eyebrow">Read-only</p>
  <h1>Configuration</h1>
  <p>Edit via the `repos` / `authors` CLIs and config.json.</p>
</section>
{#if configData}
  <div class="stack">
    <section class="surface">
      <div class="section-head"><h2>Watched repos</h2></div>
      {#if configData.repos?.length}
        <ul class="repo-list">
          {#each configData.repos as r}
            <li><span>{r.name}</span>{#if r.allowed_authors_only}<span class="tag">allowed authors only</span>{/if}</li>
          {/each}
        </ul>
      {:else}
        <div class="empty">No repos. Add with: agent-code-review repos add owner/name</div>
      {/if}
    </section>
    <section class="surface">
      <div class="section-head"><h2>Settings</h2></div>
      <div class="settings">
        {#each settingsGroups as group}
          <div class="cluster">
            <h3>{group[0]}</h3>
            {#each group[1] as row}
              <div><dt>{row[0]}</dt><dd>{row[1]}</dd></div>
            {/each}
          </div>
        {/each}
      </div>
    </section>
    <section class="surface">
      <div class="section-head"><h2>Allowed authors</h2><span>whose PRs we may approve</span></div>
      {#if authors.length}
        <div class="authors">
          <p class="authors-head"><b>Repo</b><b>GitHub</b><b>Name</b><b>Slack</b></p>
          {#each authors as a}
            <p>
              <span>
                {#if a.repo === '*'}
                  <span class="tag">all repos</span>
                {:else}
                  <a href={`https://github.com/${a.repo}`} target="_blank" rel="noopener">{a.repo}</a>
                {/if}
              </span>
              <span><a href={`https://github.com/${a.github_handle}`} target="_blank" rel="noopener">@{a.github_handle}</a></span>
              <span>{a.name || ''}</span>
              <span class="mono muted">{a.slack_id || ''}</span>
            </p>
          {/each}
        </div>
      {:else}
        <div class="empty">No allowed authors. Every PR is comment-only.</div>
      {/if}
    </section>
  </div>
{/if}
