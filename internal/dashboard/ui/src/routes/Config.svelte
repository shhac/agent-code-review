<script lang="ts">
  import { onMount } from 'svelte';
  import { getAuthors, getConfig } from '../lib/api';
  import { withFeed } from '../lib/feed';
  import PromptBox from '../lib/PromptBox.svelte';
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

  // Rows are keyed by the identity the store uses, so a handle rostered on
  // several repos expands independently per row.
  const rowKey = (a: AllowedAuthor) => `${a.repo}|${a.github_handle}`;
  let expanded = new Set<string>();
  function toggle(a: AllowedAuthor) {
    const key = rowKey(a);
    expanded.has(key) ? expanded.delete(key) : expanded.add(key);
    expanded = expanded; // reassign so Svelte sees the mutation
  }

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
  <p>Edit via the <code>repos</code> / <code>authors</code> CLIs and config.json. Run <code>authors who &lt;handle&gt; --repo &lt;owner/name&gt;</code> to see which layer decided what.</p>
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
  </div>
{/if}
