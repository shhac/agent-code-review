<script lang="ts">
  import { onMount } from 'svelte';
  import { getAuthors, getPrompt, getPromptPreview } from '../lib/api';
  import PromptBox from '../lib/PromptBox.svelte';
  import PillToggle from '../lib/PillToggle.svelte';
  import { withFeed } from '../lib/feed';
  import type { PromptResponse, PromptPreviewResponse, RuleCondition } from '../lib/types';

  const EXAMPLE_REPO = 'example-org/example-repo';
  const candidateTypes = [
    { value: 'new', label: 'New' },
    { value: 'refreshed', label: 'Refreshed' },
  ];

  let promptData: PromptResponse | null = null;
  let preview: PromptPreviewResponse | null = null;
  let handles: string[] = [];

  // Candidate the preview is assembled for; the controls drive these.
  // Author and group answer different questions and compose:
  //   neither        → what an unrostered stranger gets
  //   group only     → what anyone in that cohort gets, even with no members yet
  //   author only    → what that person ACTUALLY gets (real roster row, their
  //                    overrides fire)
  //   author + group → what they WOULD get if you moved them to that group
  let author = '';
  let group = '';
  let self = false;
  let candidateType = 'new';
  let repo = EXAMPLE_REPO;

  $: outcomes = Object.entries(promptData?.outcomes || {}).filter(([, v]) => v);
  $: repoOptions = [EXAMPLE_REPO, ...(promptData?.repos || [])];
  $: groupOptions = promptData?.groups || [];
  // Flatten a rule's condition into [key, value] pill pairs.
  $: condPairs = (when: RuleCondition | undefined): [string, string][] =>
    Object.entries(when || {}).map(([k, v]) => [k, Array.isArray(v) ? v.join(', ') : String(v)]);

  // Toggling a switch fires a fetch, and responses can land out of order, so a
  // slower earlier request could overwrite a newer one's preview and leave the
  // panel disagreeing with the switches above it. Only the latest request is
  // allowed to apply its result.
  let previewRequest = 0;
  async function loadPreview() {
    const request = ++previewRequest;
    try {
      const next = await getPromptPreview({
        author_is_gh_user: self,
        candidate_type: candidateType,
        repo,
        author: author || undefined,
        group: group || undefined,
      });
      if (request === previewRequest) preview = next;
    } catch {
      if (request === previewRequest) preview = null;
    }
  }

  // Re-assemble whenever any switch changes (also fires once on init).
  $: author, group, self, candidateType, repo, loadPreview();

  async function load() {
    promptData = await getPrompt();
    // Roster handles for the author picker. A failure here costs the picker
    // its options, never the page.
    try {
      const au = await getAuthors();
      handles = [...new Set((au.authors || []).map((a) => a.github_handle))].sort((a, b) =>
        a.toLowerCase().localeCompare(b.toLowerCase()),
      );
    } catch {
      handles = [];
    }
    return 'read-only';
  }

  onMount(withFeed(load));
</script>

<section class="page-head">
  <p class="eyebrow">Read-only</p>
  <h1>Prompt assembly</h1>
  <p>Edit the review section of config.json.</p>
</section>
{#if promptData}
  <div class="stack">
    <section class="surface"><div class="section-head"><h2>Main prompt</h2></div><PromptBox text={promptData.main_prompt || '(no main prompt configured)'} /></section>
    <section class="surface">
      <div class="section-head"><h2>Post-outcome instructions</h2><span>what the agent does after landing on each outcome</span></div>
      {#if outcomes.length}
        {#each outcomes as [k, v]}
          <div class="prompt-block"><h3>{k}</h3><PromptBox text={v as string} /></div>
        {/each}
      {:else}
        <div class="empty">None configured.</div>
      {/if}
    </section>
    <section class="surface">
      <div class="section-head"><h2>Rules</h2><span>extra instructions when their condition matches</span></div>
      {#if promptData.rules?.length}
        {#each promptData.rules as r}
          <div class="prompt-block">
            <h3>{r.name}</h3>
            <div class="cond-pills">
              {#each condPairs(r.when) as [k, v]}
                <span class="pill"><span class="pill-k">{k}</span><span class="pill-v">{v}</span></span>
              {:else}
                <span class="pill pill-any"><span class="pill-k">always</span></span>
              {/each}
            </div>
            <PromptBox text={r.prompt} />
          </div>
        {/each}
      {:else}
        <div class="empty">No rules configured.</div>
      {/if}
    </section>
    <section class="surface">
      <div class="section-head"><h2>Assembled preview</h2><span>{promptData.note}</span></div>
      <div class="preview-controls">
        <select class="repo-select" bind:value={author} title="Preview as a real rostered author: their group is read from the roster and their overrides fire">
          <option value="">any author (not rostered)</option>
          {#each handles as h}<option value={h}>@{h}</option>{/each}
        </select>
        <select class="repo-select" bind:value={group} title="Simulate membership of a group. With an author selected this previews what they WOULD get if moved here.">
          <option value="">group: from roster</option>
          {#each groupOptions as g}<option value={g.name}>{g.name} · {g.review}</option>{/each}
        </select>
        <label class="toggle" class:on={self}><input type="checkbox" bind:checked={self} /> Self-authored</label>
        <PillToggle options={candidateTypes} bind:value={candidateType} />
        <select class="repo-select" bind:value={repo}>
          {#each repoOptions as r}<option value={r}>{r}</option>{/each}
        </select>
      </div>
      {#if preview}
        <div class="trace">
          <span class="tchip matched" title="the group this candidate's author resolved to">
            <span class="tname">group</span><span class="ttgt">{preview.candidate.group}</span>
          </span>
          <span class="tchip" class:matched={preview.candidate.author_allowed}>
            <span class="tname">approve</span>
            <span class="tverdict">{preview.candidate.author_allowed ? 'permitted' : 'forbidden'}</span>
          </span>
          {#each preview.policy || [] as step}
            <span class="tchip matched" title={`${step.field} = ${step.value}`}>
              <span class="tname">{step.field}</span><span class="ttgt">{step.source}</span>
            </span>
          {/each}
        </div>
        {#if preview.rules?.length}
          <div class="trace">
            {#each preview.rules as t}
              <span class="tchip" class:matched={t.matched} title={t.reason || ''}>
                <span class="tname">{t.name}</span>
                <span class="ttgt">{t.target}</span>
                <span class="tverdict">{t.matched ? 'fires' : 'skip'}</span>
              </span>
            {/each}
          </div>
        {/if}
        <PromptBox text={preview.preview} />
      {:else}
        <div class="empty">Assembling preview…</div>
      {/if}
    </section>
  </div>
{/if}

<style>
  .cond-pills { display: flex; flex-wrap: wrap; gap: 6px; margin: 0 0 12px; }
  .pill {
    display: inline-flex; align-items: stretch; border-radius: 7px; overflow: hidden;
    border: 1px solid var(--line-strong); font-size: 11px; font-weight: 700;
    font-family: var(--mono, ui-monospace, monospace);
  }
  .pill-k { padding: 3px 8px; background: var(--surface-warm); color: var(--dim); letter-spacing: .02em; }
  .pill-v { padding: 3px 8px; background: var(--accent); color: var(--surface); }
  .pill-any .pill-k { color: var(--faint); font-style: italic; }

  .preview-controls { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; margin: 18px 20px 6px; }
  /* One shared height so the toggles, pill-toggle, and select line up. The
     :global rule is needed because PillToggle's root is a child component, so it
     doesn't carry this component's scope class (the md/raw toggle in a box keeps
     its own natural, compact size — only the preview-row instance is sized). */
  .preview-controls > * { height: 34px; box-sizing: border-box; }
  .preview-controls > :global(.pill-toggle) { height: 34px; box-sizing: border-box; }
  .toggle {
    display: inline-flex; align-items: center; gap: 8px; padding: 0 12px; cursor: pointer;
    border: 1px solid var(--line); border-radius: 8px; background: var(--surface-warm);
    color: var(--dim); font-size: 12px; font-weight: 750;
  }
  .toggle.on { border-color: var(--accent); color: var(--ink); }
  .toggle input { accent-color: var(--accent); }
  .repo-select {
    padding: 0 10px; border: 1px solid var(--line); border-radius: 8px;
    background: var(--surface-warm); color: var(--ink); font: inherit; font-size: 12px;
  }
  .repo-select:focus { outline: none; border-color: var(--accent); }

  .trace { display: flex; flex-wrap: wrap; gap: 6px; margin: 10px 20px 12px; }
  .tchip {
    display: inline-flex; align-items: center; gap: 6px; padding: 3px 4px 3px 8px;
    border-radius: 7px; border: 1px solid var(--line); background: var(--surface-warm);
    font-size: 11px; font-weight: 700; color: var(--faint);
  }
  .tchip.matched { border-color: var(--accent); color: var(--ink); }
  .tchip .tname { font-weight: 800; }
  .tchip .ttgt { color: var(--dim); text-transform: uppercase; letter-spacing: .04em; font-size: 10px; }
  .tchip .tverdict {
    padding: 2px 7px; border-radius: 5px; font-size: 10px; text-transform: uppercase;
    background: var(--line); color: var(--faint);
  }
  .tchip.matched .tverdict { background: var(--accent); color: var(--surface); }
</style>
