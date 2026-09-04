<script lang="ts">
  import { mdToHtml } from './markdown';
  import PillToggle from './PillToggle.svelte';
  import { MAX_STEERING } from './steering';

  // The steering input itself, shared by the queue's edit modal and the
  // add-with-steering form so the two cannot drift on limits or affordances.
  //
  // Markdown is the point of the write/preview toggle: the engine receives the
  // message verbatim, so a list or a code fence reaches the model as one, and
  // an author should be able to see that before saving.
  export let value = '';
  // A refusal IS the disabled state: the two were separate props that had to
  // be set together, where one could be set without the other and produce a
  // box disabled for no stated reason, or a reason with an editable box.
  // Empty means editable.
  export let refusal = '';

  $: disabled = refusal !== '';

  let mode = 'write';
  const modes = [
    { value: 'write', label: 'write' },
    { value: 'preview', label: 'preview' },
  ];

  $: remaining = MAX_STEERING - value.length;
</script>

<div class="steer-editor" class:disabled>
  <div class="steer-editor-head">
    <label for="steering-text">Steering</label>
    {#if !disabled}<PillToggle bind:value={mode} options={modes} />{/if}
  </div>

  {#if disabled}
    <div class="steer-refusal">
      <p class="status warn"><i></i>{refusal}</p>
      <textarea
        id="steering-text"
        disabled
        rows="6"
        placeholder="Only the PR's author, or the account reviews are posted as, can steer a review."
      ></textarea>
    </div>
  {:else if mode === 'preview'}
    <div class="md steer-preview">
      {#if value.trim()}{@html mdToHtml(value)}{:else}<p class="muted">Nothing to preview.</p>{/if}
    </div>
  {:else}
    <textarea
      id="steering-text"
      bind:value
      maxlength={MAX_STEERING}
      rows="10"
      placeholder="What should the next review pay attention to?&#10;&#10;Markdown works. For example:&#10;&#10;The migration is behind a flag, so focus on:&#10;- the rollback path&#10;- the `down` migration"
    ></textarea>
  {/if}

  {#if !disabled}
    <p class="steer-editor-foot">
      <small class:warn={remaining < 0}>{remaining} characters left</small>
      <small>Markdown is preserved; the reviewer is told this came from you and cannot override its instructions.</small>
    </p>
  {/if}
</div>

<style>
  .steer-editor { display: grid; gap: 8px; }
  .steer-editor-head { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
  .steer-editor-head label { font-size: 13px; text-transform: uppercase; letter-spacing: .04em; color: var(--dim); }
  .steer-editor textarea {
    width: 100%; box-sizing: border-box; resize: vertical; font: inherit;
    padding: 10px 12px; border: 1px solid var(--line); border-radius: 8px;
    background: var(--surface-warm); color: inherit; line-height: 1.5;
  }
  .steer-editor textarea:disabled { opacity: .55; cursor: not-allowed; }
  .steer-preview {
    min-height: 120px; padding: 10px 12px; border: 1px solid var(--line);
    border-radius: 8px; background: var(--surface-warm);
  }
  .steer-editor-foot { display: grid; gap: 2px; margin: 0; }
  .steer-editor-foot small { color: var(--dim); }
  .steer-editor-foot small.warn { color: var(--bad-ink); }
  .steer-refusal { display: grid; gap: 8px; }
</style>
