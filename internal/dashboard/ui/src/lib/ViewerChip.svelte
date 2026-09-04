<script lang="ts">
  import type { Viewer } from './types';

  // Who the dashboard thinks is looking. Persistent and always visible: it is
  // the only feedback a viewer gets that they were recognised, and the first
  // thing to check when steering is unexpectedly refused.
  export let viewer: Viewer | null = null;

  $: kind = !viewer ? 'dim' : viewer.anonymous ? 'warn' : viewer.handle ? 'ok' : 'warn';
  $: label = !viewer
    ? 'identifying…'
    : viewer.anonymous
      ? 'not identified'
      : viewer.handle
        ? `@${viewer.handle}`
        : viewer.login || 'unrecognised';
</script>

<div class="viewer-chip" title={viewer?.explanation ?? ''}>
  <span class="status {kind}"><i></i>{label}</span>
  {#if viewer?.handle && viewer.steer_any_pr}
    <small>can steer any PR</small>
  {:else if viewer?.handle}
    <small>can steer your own PRs</small>
  {:else if viewer && !viewer.anonymous}
    <small>no roster row for {viewer.login}</small>
  {:else if viewer}
    <small>steering needs a tailnet identity</small>
  {/if}
</div>
