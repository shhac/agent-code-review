<script lang="ts">
  import { onDestroy } from 'svelte';
  import { ago, when } from './format';
  import { steeringSession } from './steering';
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
  // The row's own identity and status. Steering is closed while a review runs:
  // that review's instructions were fixed when it was dispatched, and it never
  // re-reads the row, so an edit accepted now would reach nothing. The server
  // refuses it too; this is the same rule shown rather than a second opinion.
  export let repo = '';
  export let number = 0;
  export let reviewing = false;

  // While the editor is open the PR is parked, so a free dispatcher slot
  // cannot claim it out from under whoever is typing. The session rules live
  // in steering.ts, where they are unit-tested: this component only opens one,
  // ends it, and reports whether the PR is actually held.
  let draft = '';
  let editing = false;
  let saving = false;
  let err = '';
  let parked = false;
  let session: ReturnType<typeof steeringSession> | undefined;

  async function open() {
    draft = steering?.message ?? '';
    err = '';
    editing = true;
    session = steeringSession(repo, number);
    await session.start();
    parked = session.parked();
  }

  function close() {
    editing = false;
    parked = false;
    void session?.release();
    session = undefined;
  }

  // A tab closed mid-edit leaves the hold standing; it expires on its own,
  // which is exactly why nothing here may depend on a release arriving.
  onDestroy(() => void session?.release());

  async function save() {
    saving = true;
    err = '';
    try {
      await onsave(draft.trim());
      // The save already ended the session server-side, so close() releasing
      // again is a no-op there and cheaper than reasoning about whether it did.
      close();
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
    {#if mayEdit && !reviewing}
      <button class="linkish" on:click={() => void open()}>{steering ? 'edit' : 'add'}</button>
    {/if}
  </div>

  {#if steering}
    <div class="md steering-body">{@html mdToHtml(steering.message)}</div>
    <p class="muted">
      set by @{steering.set_by}
      <time title={when(steering.set_at)}>{ago(steering.set_at)}</time>
    </p>
  {:else if mayEdit && !reviewing}
    <p class="muted">Nothing set. An instruction here shapes the next review of this PR.</p>
  {:else if !mayEdit}
    <p class="muted">Nothing set. Only @{author} can steer this PR.</p>
  {/if}

  {#if mayEdit && reviewing}
    <p class="muted">
      {steering ? 'This instruction is shaping the review running now.' : 'No instruction was set for the review running now.'}
      Its instructions are fixed until it finishes; once it does, queue the PR again with a new one.
    </p>
  {/if}
</div>

{#if editing}
  <Modal title="Steering for this PR" onclose={close}>
    <SteeringEditor bind:value={draft} />
    {#if err}<p class="status bad"><i></i>{err}</p>{/if}
    <svelte:fragment slot="actions">
      <button class="go" on:click={save} disabled={saving}>{saving ? 'saving…' : 'Save'}</button>
      <button on:click={close} disabled={saving}>Cancel</button>
      {#if draft.trim() === '' && steering}<small class="muted">saving empty clears it</small>{/if}
      {#if !parked}<small class="muted">not held: this PR may be picked up while you write</small>{/if}
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
