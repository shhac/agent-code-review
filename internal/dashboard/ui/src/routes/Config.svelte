<script lang="ts">
  import { onMount } from 'svelte';
  import { getAuthors, getConfig } from '../lib/api';
  import { withFeed } from '../lib/feed';
  import type { AllowedAuthor, ConfigResponse } from '../lib/types';

  type SettingsGroup = [string, [string, string][]];

  // One wording for the per-daemon loop state cells (review + discovery).
  function loopState(running: boolean, enabled: boolean): string {
    if (running) return 'running';
    if (enabled) return 'off (config enabled, boot flag disabled)';
    return 'off';
  }

  // What a roster row actually grants, in one cell: the review level, plus
  // any engine dials the group or an override pinned. A row whose group config
  // no longer defines resolves to comment, which is what shows here.
  function policySummary(a: AllowedAuthor): string {
    const p = a.policy;
    if (!p) return '';
    const dials = [p.engine, p.model, p.effort].filter(Boolean).join(' / ');
    return dials ? `${p.review} (${dials})` : p.review;
  }

  let configData: ConfigResponse | null = null;
  let authors: AllowedAuthor[] = [];
  $: settingsGroups = configData ? ([
    ['Daemon', [
      ['Version', configData.version || 'dev'],
      ['Reviewing as', configData.reviewing_as ? `@${configData.reviewing_as}` : 'unknown (gh not authenticated?)'],
    ]],
    ['Review loop', [
      ['State (this daemon)', loopState(configData.review_running, configData.schedule.enabled)],
      ['Default engine', configData.engine],
      [`${configData.engine} model`, configData.engine_config.model || 'engine default'],
      [`${configData.engine} effort`, configData.engine_config.effort || 'model default'],
      ['Interval', configData.schedule.interval],
      ['Max parallel', String(configData.schedule.max_parallel)],
      ['Usage floor (5h)', configData.schedule.usage_floor_5h_percent ? `hold below ${configData.schedule.usage_floor_5h_percent}% remaining, per engine` : 'disabled'],
      ['Usage floor (weekly)', configData.schedule.usage_floor_weekly_percent ? `hold below ${configData.schedule.usage_floor_weekly_percent}% remaining, per engine` : 'disabled'],
    ]],
    ['Discovery', [
      ['State (this daemon)', loopState(configData.discovery_running, configData.discovery.enabled)],
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
  <p>Edit via the `repos` / `authors` CLIs and config.json. Run `authors who &lt;handle&gt; --repo &lt;owner/name&gt;` to see which layer decided what.</p>
</section>
{#if configData}
  <div class="stack">
    <section class="surface">
      <div class="section-head"><h2>Watched repos</h2></div>
      {#if configData.repos?.length}
        <ul class="repo-list">
          {#each configData.repos as r}
            <li>
              <span>{r.name}</span>
              {#if r.unlisted_group}
                <span class="tag" class:tag-mute={r.allowed_authors_only}>
                  unlisted &rarr; {r.unlisted_group}
                </span>
              {/if}
            </li>
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
      <div class="section-head"><h2>Author roster</h2><span>which group each author is in</span></div>
      {#if authors.length}
        <div class="authors">
          <p class="authors-head"><b>Repo</b><b>GitHub</b><b>Group</b><b>Gets</b><b>Name</b></p>
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
              <span>{a.group}</span>
              <span class="mono muted">{policySummary(a)}</span>
              <span>{a.name || ''}</span>
            </p>
          {/each}
        </div>
      {:else}
        <div class="empty">No roster entries. Every author follows authors.unlisted.</div>
      {/if}
    </section>
  </div>
{/if}
