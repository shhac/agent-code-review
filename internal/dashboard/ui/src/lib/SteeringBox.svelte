<script lang="ts">
  import { ago, when } from './format';
  import { MAX_STEERING } from './steering';
  import type { Steering } from './types';

  // The steering control for one queued PR. The editor appears only where the
  // server would accept the write, so nobody is offered a box that will 403;
  // anyone else sees the existing steering read-only, because what shaped a
  // review is worth seeing even if you cannot change it.
  //
  // mayEdit is the SERVER's answer (queueView.may_steer), not a rule
  // recomputed here. It used to be reimplemented in TypeScript, which meant
  // one rule in two languages with nothing binding them.
  export let steering: Steering | null = null;
  export let mayEdit = false;
  export let author = '';
  export let onsave: (message: string) => Promise<void>;

  let draft = '';
  let editing = false;
  let saving = false;
  let err = '';

  $: remaining = MAX_STEERING - draft.length;

  function start() {
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
  <h3>Steering</h3>

  {#if editing}
    <textarea
      bind:value={draft}
      maxlength={MAX_STEERING}
      rows="3"
      placeholder="What should the next review pay attention to?"
      aria-label="Steering message"
    ></textarea>
    <p class="steering-actions">
      <button class="go" on:click={save} disabled={saving}>{saving ? 'saving…' : 'Save'}</button>
      <button on:click={() => (editing = false)} disabled={saving}>Cancel</button>
      <small class:warn={remaining < 0}>{remaining} left</small>
      {#if draft.trim() === '' && steering}<small>saving empty clears it</small>{/if}
    </p>
    {#if err}<p class="status bad"><i></i>{err}</p>{/if}
  {:else if steering}
    <blockquote>{steering.message}</blockquote>
    <p class="muted">
      set by @{steering.set_by}
      <time title={when(steering.set_at)}>{ago(steering.set_at)}</time>
      {#if mayEdit}· <button class="linkish" on:click={start}>edit</button>{/if}
    </p>
  {:else if mayEdit}
    <p class="muted">
      Nothing set. <button class="linkish" on:click={start}>Add an instruction</button>
      for the next review of this PR.
    </p>
  {:else}
    <p class="muted">Nothing set. Only @{author} can steer this PR.</p>
  {/if}
</div>
