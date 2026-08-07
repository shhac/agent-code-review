<script lang="ts">
  import { mdToHtml } from './markdown';
  import PillToggle from './PillToggle.svelte';

  export let text: string;
  let mode = 'md'; // 'md' | 'raw'
  const modes = [
    { value: 'md', label: 'md' },
    { value: 'raw', label: 'raw' },
  ];

  // A link in a prompt is a reference to read, not somewhere to end up by
  // accident. The rendered markdown carries no href (see markdown.ts), so
  // clicking one opens this panel instead of navigating: it shows where the
  // link actually goes, which is also the answer to link text that disagrees
  // with its target.
  let shown: { text: string; url: string } | null = null;
  let copied = false;

  // Re-checked here rather than trusted from the markup: this is the one place
  // a URL becomes navigable, so it is the one place worth guarding.
  const SAFE = /^(?:https?:\/\/|mailto:)/i;
  $: visitable = shown !== null && SAFE.test(shown.url);

  function reveal(el: HTMLElement) {
    const url = el.dataset.url;
    if (!url) return;
    const label = el.textContent ?? url;
    // Clicking the same link again closes the panel.
    shown = shown?.url === url && shown.text === label ? null : { text: label, url };
    copied = false;
  }

  function onClick(e: MouseEvent) {
    const el = (e.target as HTMLElement | null)?.closest?.('.mdlink');
    if (el) reveal(el as HTMLElement);
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.key !== 'Enter' && e.key !== ' ') return;
    const el = (e.target as HTMLElement | null)?.closest?.('.mdlink');
    if (!el) return;
    e.preventDefault();
    reveal(el as HTMLElement);
  }

  async function copy() {
    if (!shown) return;
    try {
      await navigator.clipboard.writeText(shown.url);
      copied = true;
    } catch {
      copied = false;
    }
  }
</script>

<svelte:window on:keydown={(e) => e.key === 'Escape' && (shown = null)} />

<div class="pbox">
  <div class="pbox-toggle"><PillToggle options={modes} bind:value={mode} /></div>
  {#if mode === 'raw'}
    <pre>{text}</pre>
  {:else}
    <!-- Delegated: the links live inside {@html} output, so there is nothing to
         bind a handler to at render time. -->
    <!-- svelte-ignore a11y-no-static-element-interactions -->
    <div class="md" on:click={onClick} on:keydown={onKeydown}>{@html mdToHtml(text)}</div>
    {#if shown}
      <div class="linkbar">
        <span class="linkbar-label">{shown.text}</span>
        <input class="linkbar-url" readonly value={shown.url} aria-label="Link target" />
        <button class="linkbar-btn" on:click={copy} title="Copy link">{copied ? 'copied' : 'copy'}</button>
        {#if visitable}
          <a
            class="linkbar-btn linkbar-go"
            href={shown.url}
            target="_blank"
            rel="noopener noreferrer"
            title="Open in a new tab"
            aria-label="Open {shown.url} in a new tab"
          >
            <svg viewBox="0 0 16 16" width="13" height="13" aria-hidden="true" focusable="false">
              <path d="M6.5 2.5h-4v11h11v-4" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
              <path d="M9.5 2.5h4v4M13.5 2.5l-6 6" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
          </a>
        {/if}
        <button class="linkbar-btn linkbar-close" on:click={() => (shown = null)} aria-label="Dismiss">×</button>
      </div>
    {/if}
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
  /* Inert by design: no href, so a stray click cannot navigate. The dotted
     underline says "there is a target here" without promising a jump. */
  .md :global(.mdlink) {
    color: var(--accent); font-weight: 700; cursor: pointer;
    text-decoration: underline; text-decoration-style: dotted; text-underline-offset: 2px;
  }
  .md :global(.mdlink:hover), .md :global(.mdlink:focus-visible) {
    text-decoration-style: solid; outline: none;
  }

  .linkbar {
    display: flex; align-items: center; gap: 8px; padding: 8px 12px;
    border-top: 1px solid rgba(255, 255, 255, 0.14); background: rgba(255, 255, 255, 0.04);
  }
  .linkbar-label { font-size: 12px; font-weight: 750; color: var(--accent); flex: 0 0 auto; max-width: 30%;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .linkbar-url {
    flex: 1 1 auto; min-width: 0; padding: 4px 8px; border-radius: 6px;
    border: 1px solid rgba(255, 255, 255, 0.16); background: rgba(0, 0, 0, 0.3);
    color: #eef1ee; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px;
  }
  .linkbar-btn {
    flex: 0 0 auto; display: inline-flex; align-items: center; justify-content: center;
    height: 24px; padding: 0 8px; border-radius: 6px; cursor: pointer;
    border: 1px solid rgba(255, 255, 255, 0.16); background: transparent;
    color: #cfd6cf; font: inherit; font-size: 11px; font-weight: 750; text-decoration: none;
  }
  .linkbar-btn:hover { color: #fff; border-color: var(--accent); }
  .linkbar-go { padding: 0 7px; }
  .linkbar-close { font-size: 14px; line-height: 1; }

  .md :global(hr) {
    border: 0; border-top: 1px solid rgba(255, 255, 255, 0.16); margin: 14px 0;
  }
  .md :global(pre) { background: var(--paper); padding: 12px 14px; border-radius: 8px; overflow-x: auto; margin: 8px 0; }
  .md :global(pre code) { background: none; padding: 0; }
</style>
