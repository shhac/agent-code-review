<script lang="ts">
  import { rosterView } from './configview';
  import { toggleIn } from './expandable';
  import PromptBox from './PromptBox.svelte';
  import type { AllowedAuthor } from './types';

  export let authors: AllowedAuthor[];
  // Bindable so the page can hold them: this table unmounts with its tab, and
  // the filters and open rows should still be there on the way back.
  export let repoFilter = '';
  export let groupFilter = '';
  export let expanded = new Set<string>();

  // What a roster row actually grants, in one cell: the review level, plus
  // any engine dials the group or an override pinned. A row whose group config
  // no longer defines resolves to comment, which is what shows here.
  function policySummary(a: AllowedAuthor): string {
    const p = a.policy;
    if (!p) return '';
    const dials = [p.engine, p.model, p.effort].filter(Boolean).join(' / ');
    return dials ? `${p.review} (${dials})` : p.review;
  }

  // Rows are keyed by the identity the store uses, so a handle rostered on
  // several repos expands independently per row.
  const rowKey = (a: AllowedAuthor) => `${a.repo}|${a.github_handle}`;
  const toggle = (a: AllowedAuthor) => (expanded = toggleIn(expanded, rowKey(a)));

  $: ({ repoOptions, groupOptions, visible: visibleAuthors } = rosterView(authors, repoFilter, groupFilter));

  // The dials a row inherits rather than sets are worth showing as inherited,
  // not as blank: "no override" and "unknown" look identical otherwise.
  const dials = (a: AllowedAuthor): [string, string][] => [
    ['Engine', a.policy?.engine || 'inherits the configured default'],
    ['Model', a.policy?.model || 'inherits the engine default'],
    ['Effort', a.policy?.effort || 'inherits the engine default'],
  ];
</script>

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
