<script lang="ts">
  import { codeSpans, scoringDialsShown, settingsGroups } from '../lib/configview';
  import { tierRanges } from '../lib/scoreshape';
  import { onMount } from 'svelte';
  import { getAuthors, getConfig } from '../lib/api';
  import { withFeed } from '../lib/feed';
  import RosterTable from '../lib/RosterTable.svelte';
  import ScoreCalculator from '../lib/ScoreCalculator.svelte';
  import ScoreShape from '../lib/ScoreShape.svelte';
  import type { AllowedAuthor, ConfigResponse } from '../lib/types';

  let configData: ConfigResponse | null = null;
  let authors: AllowedAuthor[] = [];
  let repoFilter = '';
  let groupFilter = '';
  let expandedAuthors = new Set<string>();

  $: groups = settingsGroups(configData);
  $: tiers = tierRanges(configData?.scoring.tiers ?? []);

  // Three pages' worth of panels, split by who is asking. The roster is the
  // people config; settings and the ladder are what the reviewer will do with
  // it; the tools are for working out what it SHOULD do, which is a job you
  // do once and then leave alone.
  const TABS = [
    { id: 'roster', label: 'Repos & authors' },
    { id: 'settings', label: 'Settings' },
    { id: 'tuning', label: 'Score tuning' },
  ];
  let tab = 'roster';

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
  <p>Edit via the <code>repos</code> / <code>authors</code> CLIs and config.json. Run <code>authors who &lt;handle&gt; --repo &lt;owner/name&gt;</code> to see which layer decided what.</p>
</section>
{#if configData}
  <div class="page-tabs" role="tablist" aria-label="Configuration sections">
    {#each TABS as t}
      <button
        role="tab"
        id={`tab-${t.id}`}
        aria-selected={tab === t.id}
        aria-controls={`panel-${t.id}`}
        on:click={() => (tab = t.id)}
      >{t.label}</button>
    {/each}
  </div>
  <div class="stack" role="tabpanel" id={`panel-${tab}`} aria-labelledby={`tab-${tab}`} tabindex="-1">
    {#if tab === 'roster'}
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
    {/if}
    {#if tab === 'settings'}
    <section class="surface">
      <div class="section-head"><h2>Settings</h2></div>
      <div class="settings">
        {#each groups as group}
          <div class="cluster">
            <h3>{group[0]}</h3>
            {#each group[1] as row}
              <div><dt>{row[0]}</dt><dd>{#each codeSpans(row[1]) as span}{#if span.code}<code>{span.text}</code>{:else}{span.text}{/if}{/each}</dd></div>
            {/each}
          </div>
        {/each}
      </div>
    </section>
    {#if scoringDialsShown(configData)}
      <section class="surface">
        <div class="section-head">
          <h2>Score shape</h2>
          <span>what a PR earns for its shape</span>
        </div>
        {#if configData.scoring.scoped_repos.length}
          <p class="scoped-note">
            The global policy. {configData.scoring.scoped_repos.join(', ')}
            {configData.scoring.scoped_repos.length === 1 ? 'scores' : 'score'} under their own rules.
          </p>
        {/if}
        <div class="tier-panel">
          <dl class="shape-summary">
            <div><dt>Best piece size</dt><dd>{configData.scoring.piece_lines} changed lines</dd></div>
            <div><dt>Best single PR</dt><dd>{Math.round(configData.scoring.peak)} changed lines</dd></div>
            <div><dt>Worth, at the peak</dt><dd>{configData.scoring.size_points} points</dd></div>
            <div><dt>Removing code</dt><dd>{configData.scoring.removal_points_per_100} points per 100 net lines</dd></div>
          </dl>
          <table class="tier-table">
            <thead><tr><th>Tier</th><th>Changed lines</th></tr></thead>
            <tbody>
              {#each tiers as t}
                <tr><td>{t.name}</td><td class="mono">{t.range}</td></tr>
              {/each}
            </tbody>
          </table>
        </div>
      </section>
    {/if}
    {/if}
    {#if tab === 'tuning'}
      {#if scoringDialsShown(configData)}
        <section class="surface">
          <div class="section-head">
            <h2>Score calculator</h2>
            <span>live global policy; the draft below does not change this calculator</span>
          </div>
          <ScoreCalculator />
        </section>
        <section class="surface">
          <div class="section-head">
            <h2>Score shape</h2>
            <span>what a policy would pay, before you ship it</span>
          </div>
          <ScoreShape config={configData} />
        </section>
      {:else}
        <div class="empty">Scoring is off, so there is nothing to tune. Set scoring.mode to enabled first.</div>
      {/if}
    {/if}
    {#if tab === 'roster'}
      <RosterTable {authors} bind:repoFilter bind:groupFilter bind:expanded={expandedAuthors} />
    {/if}
  </div>
{/if}
