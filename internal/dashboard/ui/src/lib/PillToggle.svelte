<script lang="ts">
  // A compact segmented button-pair (the md/raw and new/refreshed switches).
  // Fills its container's height, so it sizes to a code-box corner or a control
  // row alike. bind:value to the selected option value.
  export let options: { value: string; label: string }[];
  export let value: string;
</script>

<div class="pill-toggle">
  {#each options as o}
    <button type="button" class:active={value === o.value} on:click={() => (value = o.value)}>{o.label}</button>
  {/each}
</div>

<style>
  .pill-toggle { display: inline-flex; height: 100%; }
  .pill-toggle button {
    display: inline-flex; align-items: center; justify-content: center; height: 100%;
    font-size: 11px; font-weight: 750; letter-spacing: .04em; line-height: 1; padding: 4px 11px;
    border: 1px solid var(--line); background: var(--surface-warm); color: var(--dim);
    cursor: pointer; text-transform: uppercase; white-space: nowrap;
  }
  .pill-toggle button:first-child { border-radius: 6px 0 0 6px; }
  .pill-toggle button:last-child { border-radius: 0 6px 6px 0; border-left: 0; }
  .pill-toggle button.active { background: var(--accent); color: var(--surface); border-color: var(--accent); }
</style>
