<script lang="ts">
  import { prHref } from './format';
  import { scoreLabel, scoreTitle } from './leaderboard';

  export let repo: string;
  export let number: number;
  export let url: string | undefined = undefined;
  export let title = '';
  export let author = '';
  // What the review earned the author. undefined means unscored, which is not
  // the same as 0, so the two render differently and neither is invented for
  // a surface (the queue) where no review has happened yet.
  export let score: number | undefined = undefined;
  export let bucket = '';
</script>

<span class="ticket-id">
  <a class="ticket-pr" href={prHref(repo, number, url)} target="_blank" rel="noopener" on:click|stopPropagation>#{number}</a>
  {#if score !== undefined}
    <small class="ticket-score" class:negative={score < 0} title={scoreTitle(score, bucket)}>{scoreLabel(score)}</small>
  {/if}
</span>
<span class="ticket-copy">
  <strong>{title || '(title not recorded)'}</strong>
  <small>{repo}{author ? ` · @${author}` : ''}</small>
</span>

<style>
  /* The id column stacks the number over what the review earned, which fits
     the space the number already reserved rather than claiming a new column. */
  .ticket-id { display: grid; justify-items: start; gap: 2px; }
  .ticket-score {
    font-size: 11px;
    font-weight: 800;
    font-variant-numeric: tabular-nums;
    color: var(--accent);
    letter-spacing: 0.02em;
  }
  .ticket-score.negative { color: var(--bad, #e5534b); }
</style>
