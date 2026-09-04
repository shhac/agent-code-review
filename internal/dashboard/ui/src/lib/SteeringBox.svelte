<script lang="ts">
  import { ago, when } from './format';
  import { mdToHtml } from './markdown';
  import Modal from './Modal.svelte';
  import SteeringEditor from './SteeringEditor.svelte';
  import type { Steering } from './types';

  // One queued PR's steering, read full-width inside the accordion and edited
  // in a modal. Reading is the common case and wants the width; writing is
  // rare and wants the room, which a column shared with the review history
  // could not give either of them.
  //
  // mayEdit is the SERVER's answer (queueView.may_steer). The rule is not
  // recomputed here: it used to be, which meant one rule in two languages with
  // nothing binding them.
  export let steering: Steering | null = null;
  export let mayEdit = false;
  export let author = '';
  export let onsave: (message: string) => Promise<void>;

  let draft = '';
  let editing = false;
  let saving = false;
  let err = '';

  function open() {
    draft = steering?.message ?? '';
    err = '';
    editing = true;
  }

  async function save() {
    saving = true;
    err = '';
    try {
      await onsave(draft.trim());
      editing = false;
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    } finally {
      saving = false;
    }
  }
</script>

<div class="steering">
  <div class="steering-head">
    <h3>Steering</h3>
    {#if mayEdit}
      <button class="linkish" on:click={open}>{steering ? 'edit' : 'add'}</button>
    {/if}
  </div>

  {#if steering}
    <div class="md steering-body">{@html mdToHtml(steering.message)}</div>
    <p class="muted">
      set by @{steering.set_by}
      <time title={when(steering.set_at)}>{ago(steering.set_at)}</time>
    </p>
  {:else if mayEdit}
    <p class="muted">Nothing set. An instruction here shapes the next review of this PR.</p>
  {:else}
    <p class="muted">Nothing set. Only @{author} can steer this PR.</p>
  {/if}
</div>

{#if editing}
  <Modal title="Steering for this PR" onclose={() => (editing = false)}>
    <SteeringEditor bind:value={draft} />
    {#if err}<p class="status bad"><i></i>{err}</p>{/if}
    <svelte:fragment slot="actions">
      <button class="go" on:click={save} disabled={saving}>{saving ? 'saving…' : 'Save'}</button>
      <button on:click={() => (editing = false)} disabled={saving}>Cancel</button>
      {#if draft.trim() === '' && steering}<small class="muted">saving empty clears it</small>{/if}
    </svelte:fragment>
  </Modal>
{/if}

<style>
  .steering { margin-top: 16px; }
  .steering-head { display: flex; gap: 10px; align-items: baseline; }
  .steering-head h3 {
    margin: 0 0 6px; font-size: 13px; text-transform: uppercase;
    letter-spacing: .04em; color: var(--dim);
  }
  .steering-body {
    padding: 10px 14px; border-left: 3px solid var(--accent);
    background: var(--surface-warm); border-radius: 0 8px 8px 0;
  }
</style>
