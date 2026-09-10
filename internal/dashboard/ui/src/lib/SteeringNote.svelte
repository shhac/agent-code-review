<script lang="ts">
  // One steering instruction, rendered read-only.
  //
  // Shared by the queue accordion (where the message can then be edited) and
  // the history accordion (where it never can). The message is markdown
  // because the engine receives it verbatim, so what is shown has to be what
  // the engine will read, not the asterisks around it.
  import { ago, when } from './format';
  import { mdToHtml } from './markdown';
  import type { Steering } from './types';

  export let steering: Steering;
</script>

<div class="md steering-body">{@html mdToHtml(steering.message)}</div>
<p class="muted">
  set by @{steering.set_by}
  <time title={when(steering.set_at)}>{ago(steering.set_at)}</time>
</p>

<style>
  .steering-body {
    padding: 10px 14px;
    border-left: 3px solid var(--accent);
    background: var(--surface-warm);
    border-radius: 0 8px 8px 0;
  }
</style>
