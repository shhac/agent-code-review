<script lang="ts">
  import { prHref } from './format';

  export let repo: string;
  export let number: number;
  export let url: string | undefined = undefined;
  export let title = '';
  export let author = '';
</script>

<a class="ticket-pr" href={prHref(repo, number, url)} target="_blank" rel="noopener" on:click|stopPropagation>#{number}</a>
<span class="ticket-copy">
  <strong>{title || '(title not recorded)'}</strong>
  <small>{repo}{author ? ` · @${author}` : ''}</small>
</span>
