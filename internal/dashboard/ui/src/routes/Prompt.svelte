<script lang="ts">
  import { onMount } from 'svelte';
  import { getPrompt, getPromptPreview } from '../lib/api';
  import PromptBox from '../lib/PromptBox.svelte';
  import { feedLive, feedStale } from '../lib/feed';
  import type { PromptResponse, PromptPreviewResponse, RuleCondition } from '../lib/types';

  const EXAMPLE_REPO = 'example-org/example-repo';

  let promptData: PromptResponse | null = null;
  let preview: PromptPreviewResponse | null = null;

  // Candidate the preview is assembled for; the switches drive these.
  let allowed = true;
  let self = false;
  let candidateType = 'new';
  let repo = EXAMPLE_REPO;

  $: outcomes = Object.entries(promptData?.outcomes || {}).filter(([, v]) => v);
  $: repoOptions = [EXAMPLE_REPO, ...(promptData?.repos || [])];
  // Flatten a rule's condition into [key, value] pill pairs.
  $: condPairs = (when: RuleCondition | undefined): [string, string][] =>
    Object.entries(when || {}).map(([k, v]) => [k, Array.isArray(v) ? v.join(', ') : String(v)]);

  async function loadPreview() {
    try {
      preview = await getPromptPreview({
        author_allowed: allowed,
        author_is_gh_user: self,
        candidate_type: candidateType,
        repo,
      });
    } catch {
      preview = null;
    }
  }

  // Re-assemble whenever any switch changes (also fires once on init).
  $: allowed, self, candidateType, repo, loadPreview();

  async function load() {
    try {
      promptData = await getPrompt();
      feedLive('read-only');
    } catch {
      feedStale();
    }
  }

  onMount(load);
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
        <label class="toggle" class:on={allowed}><input type="checkbox" bind:checked={allowed} /> Author allowed</label>
        <label class="toggle" class:on={self}><input type="checkbox" bind:checked={self} /> Self-authored</label>
        <div class="segmented compact">
          <label><input type="radio" bind:group={candidateType} value="new" /> New</label>
          <label><input type="radio" bind:group={candidateType} value="refreshed" /> Refreshed</label>
        </div>
        <select class="repo-select" bind:value={repo}>
          {#each repoOptions as r}<option value={r}>{r}</option>{/each}
        </select>
      </div>
      {#if preview}
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
  .toggle {
    display: inline-flex; align-items: center; gap: 8px; padding: 7px 12px; cursor: pointer;
    border: 1px solid var(--line); border-radius: 8px; background: var(--surface-warm);
    color: var(--dim); font-size: 12px; font-weight: 750;
  }
  .toggle.on { border-color: var(--accent); color: var(--ink); }
  .toggle input { accent-color: var(--accent); }
  .segmented.compact { margin: 0; }
  .repo-select {
    padding: 7px 10px; border: 1px solid var(--line); border-radius: 8px;
    background: var(--surface-warm); color: var(--ink); font: inherit; font-size: 12px;
  }
  .repo-select:focus { outline: none; border-color: var(--accent); }

  .trace { display: flex; flex-wrap: wrap; gap: 6px; margin: 10px 20px 0; }
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
