<script lang="ts">
  import { codeSpans, scoringDialsShown, settingsGroups } from '../lib/configview';
  import { tierRanges } from '../lib/scoreshape';
  import { toggleIn } from '../lib/expandable';
  import { onMount } from 'svelte';
  import { getAuthors, getConfig } from '../lib/api';
  import { withFeed } from '../lib/feed';
  import PromptBox from '../lib/PromptBox.svelte';
  import ScoreCalculator from '../lib/ScoreCalculator.svelte';
  import ScoreShape from '../lib/ScoreShape.svelte';
  import type { AllowedAuthor, ConfigResponse } from '../lib/types';



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

  // Rows are keyed by the identity the store uses, so a handle rostered on
  // several repos expands independently per row.
  const rowKey = (a: AllowedAuthor) => `${a.repo}|${a.github_handle}`;
  let expanded = new Set<string>();
  const toggle = (a: AllowedAuthor) => (expanded = toggleIn(expanded, rowKey(a)));

  // The dials a row inherits rather than sets are worth showing as inherited,
  // not as blank: "no override" and "unknown" look identical otherwise.
  // Filters are derived from the ROWS, not from config, so every option on
  // offer yields at least one result: a filter that can select nothing is
  // noise. Counts come along for free and say how big each cohort is.
  let repoFilter = '';
  let groupFilter = '';
  const tally = (rows: AllowedAuthor[], of: (a: AllowedAuthor) => string) => {
    const counts = new Map<string, number>();
    for (const a of rows) counts.set(of(a), (counts.get(of(a)) || 0) + 1);
    return [...counts.entries()].sort((x, y) => x[0].toLowerCase().localeCompare(y[0].toLowerCase()));
  };
  // Each filter's options are tallied over what the OTHER filter leaves, so a
  // combination that would show nothing is never offered.
  $: repoOptions = tally(
    authors.filter((a) => !groupFilter || a.group === groupFilter),
    (a) => a.repo,
  );
  $: groupOptions = tally(
    authors.filter((a) => !repoFilter || a.repo === repoFilter),
    (a) => a.group,
  );
  $: visibleAuthors = authors.filter(
    (a) => (!repoFilter || a.repo === repoFilter) && (!groupFilter || a.group === groupFilter),
  );

  const dials = (a: AllowedAuthor): [string, string][] => [
    ['Engine', a.policy?.engine || 'inherits the configured default'],
    ['Model', a.policy?.model || 'inherits the engine default'],
    ['Effort', a.policy?.effort || 'inherits the engine default'],
  ];
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
            <span>scored by the daemon, not estimated here</span>
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
    <section class="surface">
      <div class="section-head">
        <h2>Author roster</h2>
        <span>
          {#if visibleAuthors.length === authors.length}
            {authors.length} rostered · click a row for details
          {:else}
            {visibleAuthors.length} of {authors.length} · click a row for details
          {/if}
        </span>
      </div>
      {#if authors.length}
        <div class="roster-controls">
          <select class="roster-select" bind:value={repoFilter} title="Show only rows rostered for this repo">
            <option value="">all repos</option>
            {#each repoOptions as [name, n]}
              <option value={name}>{name === '*' ? 'all repos (*)' : name} · {n}</option>
            {/each}
          </select>
          <select class="roster-select" bind:value={groupFilter} title="Show only authors in this group">
            <option value="">all groups</option>
            {#each groupOptions as [name, n]}<option value={name}>{name} · {n}</option>{/each}
          </select>
          {#if repoFilter || groupFilter}
            <button class="roster-clear" on:click={() => { repoFilter = ''; groupFilter = ''; }}>clear</button>
          {/if}
        </div>
        <div class="authors">
          <p class="authors-head"><b>Repo</b><b>GitHub</b><b>Group</b><b>Gets</b><b>Name</b></p>
          {#each visibleAuthors as a}
            <p
              class="author-row"
              class:open={expanded.has(rowKey(a))}
              role="button"
              tabindex="0"
              aria-expanded={expanded.has(rowKey(a))}
              title="Show contact details and the full resolved policy"
              on:click={() => toggle(a)}
              on:keydown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault();
                  toggle(a);
                }
              }}
            >
              <span>
                <span class="chev" aria-hidden="true">{expanded.has(rowKey(a)) ? '▾' : '▸'}</span>
                {#if a.repo === '*'}
                  <span class="tag">all repos</span>
                {:else}
                  <!-- Links must not toggle the row they sit in. -->
                  <a href={`https://github.com/${a.repo}`} target="_blank" rel="noopener" on:click|stopPropagation>{a.repo}</a>
                {/if}
              </span>
              <span><a href={`https://github.com/${a.github_handle}`} target="_blank" rel="noopener" on:click|stopPropagation>@{a.github_handle}</a></span>
              <span>{a.group}</span>
              <span class="mono muted">{policySummary(a)}</span>
              <span>{a.name || ''}</span>
            </p>
            {#if expanded.has(rowKey(a))}
              <div class="author-detail">
                <dl>
                  <div><dt>Email</dt><dd>{#if a.email}<a href={`mailto:${a.email}`}>{a.email}</a>{:else}<span class="muted">none recorded</span>{/if}</dd></div>
                  <div><dt>Slack ID</dt><dd class="mono">{a.slack_id || '—'}</dd></div>
                  <div><dt>Review level</dt><dd>{a.policy?.review || '—'}</dd></div>
                  {#each dials(a) as [label, value]}
                    <div><dt>{label}</dt><dd class:muted={value.startsWith('inherits')}>{value}</dd></div>
                  {/each}
                </dl>
                {#if a.policy?.prompt}
                  <div class="detail-prompt">
                    <p class="detail-label">Extra prompt <span class="muted">— appended to this author's reviews, from their group and any override</span></p>
                    <PromptBox text={a.policy.prompt} />
                  </div>
                {/if}
              </div>
            {/if}
          {/each}
        </div>
      {:else}
        <div class="empty">No roster entries. Every author follows authors.unlisted.</div>
      {/if}
      {#if authors.length && !visibleAuthors.length}
        <div class="empty">No authors match this filter.</div>
      {/if}
    </section>
    {/if}
  </div>
{/if}
