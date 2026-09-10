<script lang="ts">
  // Queue a PR with an optional instruction, given a reference already
  // resolved by preflight.
  //
  // Shared by the two places a PR gets queued by hand: the Overview add box,
  // and re-running a review from history. The steering half is why it is
  // shared rather than duplicated — a manual add lands on a queue the
  // dispatcher may drain within one idle poll, so the instruction has to ride
  // on the same write, and the refusal it can come back with has to be
  // reported rather than swallowed.
  import Modal from './Modal.svelte';
  import SteeringEditor from './SteeringEditor.svelte';
  import { queuePR } from './api';
  import type { QueuePreflight } from './types';

  export let pr: QueuePreflight;
  export let url: string;
  export let title: string;
  /** Prefills the editor: history passes whatever steered the last review. */
  export let initial = '';
  export let onclose: () => void;
  /** Reports the outcome: '' when everything asked for happened. */
  export let ondone: (note: string) => void;

  let draft = initial;
  let busy = false;
  let err = '';

  async function queue() {
    busy = true;
    err = '';
    try {
      // Only send an instruction the server said this viewer may set. It
      // re-checks anyway; this keeps the request honest about what was asked.
      const res = await queuePR(url, pr.may_steer ? draft.trim() : '');
      onclose();
      // The PR was queued either way, so a refusal is reported rather than
      // raised: half of what was asked for happened.
      ondone(res.steering_refused ? `Queued, but not steered: ${res.steering_refused}` : '');
    } catch (e) {
      err = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }
</script>

<Modal {title} {onclose}>
  <p class="muted">{pr.title} · by @{pr.author}</p>
  <SteeringEditor bind:value={draft} refusal={pr.may_steer ? '' : (pr.refusal ?? '')} />
  {#if err}<p class="status bad"><i></i>{err}</p>{/if}
  <svelte:fragment slot="actions">
    <button class="go" on:click={queue} disabled={busy}>
      {busy ? 'queueing…' : pr.may_steer ? 'Queue with steering' : 'Queue anyway'}
    </button>
    <button on:click={onclose} disabled={busy}>Cancel</button>
  </svelte:fragment>
</Modal>
