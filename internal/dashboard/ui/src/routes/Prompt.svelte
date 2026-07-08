<script lang="ts">
  import { onMount } from 'svelte';
  import { getPrompt } from '../lib/api';
  import { feedLive, feedStale } from '../lib/feed';
  import type { PromptResponse } from '../lib/types';

  let promptData: PromptResponse | null = null;
  let previewVariant = 'allowed_author';

  $: activePreview = promptData?.previews?.[previewVariant] || '';
  $: outcomes = Object.entries(promptData?.outcomes || {}).filter(([, v]) => v);

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
    <section class="surface"><div class="section-head"><h2>Main prompt</h2></div><pre>{promptData.main_prompt || '(no main prompt configured)'}</pre></section>
    <section class="surface">
      <div class="section-head"><h2>Post-outcome instructions</h2><span>what the agent does after landing on each outcome</span></div>
      {#if outcomes.length}
        {#each outcomes as [k, v]}
          <div class="prompt-block"><h3>{k}</h3><pre>{v as string}</pre></div>
        {/each}
      {:else}
        <div class="empty">None configured.</div>
      {/if}
    </section>
    <section class="surface">
      <div class="section-head"><h2>Rules</h2><span>extra instructions when their condition matches</span></div>
      {#if promptData.rules?.length}
        {#each promptData.rules as r}
          <div class="prompt-block"><h3>{r.name}</h3><p class="muted">when: <code>{JSON.stringify(r.when || {})}</code></p><pre>{r.prompt}</pre></div>
        {/each}
      {:else}
        <div class="empty">No rules configured.</div>
      {/if}
    </section>
    <section class="surface">
      <div class="section-head"><h2>Assembled preview</h2><span>{promptData.note}</span></div>
      <div class="segmented">
        <label><input type="radio" bind:group={previewVariant} value="allowed_author" /> Allowed author</label>
        <label><input type="radio" bind:group={previewVariant} value="not_allowed_author" /> Author not allowed</label>
      </div>
      <pre>{activePreview}</pre>
    </section>
  </div>
{/if}
