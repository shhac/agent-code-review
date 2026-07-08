<script lang="ts">
  import { statusKind, statusLabel } from './format';

  // The standard status chip: colored pulse dot + label from the shared
  // status vocabulary. Chips needing extra content (elapsed suffixes, link
  // wrappers, literal classes) stay hand-rolled at their sites.
  export let status: string;
</script>

<span class="status {statusKind(status)}"><i></i>{statusLabel(status)}</span>
