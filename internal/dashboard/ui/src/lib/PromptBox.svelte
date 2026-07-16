<script lang="ts">
  import { mdToHtml } from './markdown';
  import PillToggle from './PillToggle.svelte';

  export let text: string;
  let mode = 'md'; // 'md' | 'raw'
  const modes = [
    { value: 'md', label: 'md' },
    { value: 'raw', label: 'raw' },
  ];
</script>

<div class="pbox">
  <div class="pbox-toggle"><PillToggle options={modes} bind:value={mode} /></div>
  {#if mode === 'raw'}
    <pre>{text}</pre>
  {:else}
    <div class="md">{@html mdToHtml(text)}</div>
  {/if}
</div>

<style>
  .pbox { position: relative; background: #1a1d1b; color: #eef1ee; }
  .pbox pre {
    margin: 0; padding: 18px 20px; background: transparent; border: 0;
    white-space: pre-wrap; word-break: break-word; font-size: 13px; line-height: 1.6;
  }

  /* Overlay the md/raw switch in the box's top-right corner. */
  .pbox-toggle { position: absolute; top: 8px; right: 10px; z-index: 1; }

  /* Rendered markdown (styles reach {@html} output via :global). */
  .md { padding: 14px 20px 18px; line-height: 1.55; font-size: 14px; }
  .md :global(h1), .md :global(h2), .md :global(h3), .md :global(h4) {
    margin: 16px 0 6px; color: var(--accent); font-weight: 800; text-transform: none; letter-spacing: 0;
  }
  .md :global(h1) { font-size: 17px; }
  .md :global(h2) { font-size: 15px; }
  .md :global(h3), .md :global(h4) { font-size: 14px; }
  .md :global(:first-child) { margin-top: 0; }
  .md :global(p) { margin: 8px 0; }
  .md :global(ul), .md :global(ol) { margin: 8px 0; padding-left: 22px; }
  .md :global(li) { margin: 3px 0; }
  .md :global(strong) { color: #fff; font-weight: 800; }
  .md :global(code) {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12.5px;
    background: var(--surface-warm); padding: 1px 5px; border-radius: 4px;
  }
  .md :global(pre) { background: var(--paper); padding: 12px 14px; border-radius: 8px; overflow-x: auto; margin: 8px 0; }
  .md :global(pre code) { background: none; padding: 0; }
</style>
