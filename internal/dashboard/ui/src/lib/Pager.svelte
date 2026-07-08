<script lang="ts">
  // The standard ‹ n/total › pager. `page` is zero-based and parent-bound;
  // the control hides itself when there's only one page.
  export let page = 0;
  export let pageCount: number;
</script>

{#if pageCount > 1}
  <span class="pager">
    <button type="button" disabled={page === 0} on:click={() => (page -= 1)}>‹</button>
    {page + 1}/{pageCount}
    <button type="button" disabled={page >= pageCount - 1} on:click={() => (page += 1)}>›</button>
  </span>
{/if}
