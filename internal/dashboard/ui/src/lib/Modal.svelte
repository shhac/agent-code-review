<script lang="ts">
  // A centred dialog for the two flows that need more room than the accordion
  // gives: editing steering, and adding a PR with steering. Escape and a
  // backdrop click both close, because a modal you cannot dismiss by reflex is
  // worse than no modal.
  export let title = '';
  export let onclose: () => void;

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') onclose();
  }
</script>

<svelte:window on:keydown={onKeydown} />

<div
  class="modal-backdrop"
  role="button"
  tabindex="-1"
  aria-label="Close"
  on:click|self={onclose}
  on:keydown={(e) => { if (e.key === 'Enter') onclose(); }}
>
  <div class="modal" role="dialog" aria-modal="true" aria-label={title}>
    <div class="modal-head">
      <h2>{title}</h2>
      <button class="modal-x" aria-label="Close" on:click={onclose}>×</button>
    </div>
    <div class="modal-body"><slot /></div>
    <div class="modal-foot"><slot name="actions" /></div>
  </div>
</div>

<style>
  .modal-backdrop {
    position: fixed; inset: 0; z-index: 50; display: grid; place-items: center;
    background: rgba(0, 0, 0, .45); padding: 24px; border: 0;
  }
  .modal {
    width: min(760px, 100%); max-height: min(80vh, 100%); overflow: auto;
    background: var(--surface); border: 1px solid var(--line); border-radius: 12px;
    padding: 20px 22px; display: grid; gap: 14px;
  }
  .modal-head { display: flex; justify-content: space-between; align-items: baseline; gap: 16px; }
  .modal-head h2 { margin: 0; }
  .modal-x { background: none; border: 0; font-size: 22px; line-height: 1; cursor: pointer; color: var(--dim); }
  .modal-foot { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
</style>
