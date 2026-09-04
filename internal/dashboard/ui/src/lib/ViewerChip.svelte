<script lang="ts">
  import type { Viewer } from './types';

  // Who the dashboard thinks is looking. Persistent and always visible: it is
  // the only feedback a viewer gets that they were recognised, and the first
  // thing to check when steering is unexpectedly refused.
  //
  // One table, keyed on the state the server named. The three presentation
  // facts (dot colour, label, note) used to be three separate ternary or {#if}
  // chains that tested the same cases in different orders, so a new state
  // meant three edits and nothing kept them agreeing.
  export let viewer: Viewer | null = null;

  const chip = {
    anonymous: { kind: 'warn', label: 'not identified', note: 'steering needs a tailnet identity' },
    unmapped:  { kind: 'warn', label: '',               note: 'no roster row for this login' },
    author:    { kind: 'ok',   label: '',               note: 'can steer your own PRs' },
    operator:  { kind: 'ok',   label: '',               note: 'can steer any PR' },
  } as const;

  $: c = viewer ? chip[viewer.state] : { kind: 'dim', label: 'identifying…', note: '' };
  // unmapped shows the login (nothing else identifies them); the rostered
  // states show the handle, which is what authorisation actually keys on.
  $: label = c.label || (viewer?.handle ? `@${viewer.handle}` : viewer?.login || 'unrecognised');
</script>

<div class="viewer-chip">
  <span class="status {c.kind}"><i></i>{label}</span>
  {#if c.note}<small>{c.note}</small>{/if}
</div>
