<script lang="ts">
  import { prHref, scoreLabel, scoreTitle } from './format';

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
  // Whether these points are the viewer's own, which is the only thing that
  // animates. Seeing your own score move is the reward; seeing everybody
  // else's move is a page that will not sit still.
  export let mine = false;
</script>

<span class="ticket-id">
  <a class="ticket-pr" href={prHref(repo, number, url)} target="_blank" rel="noopener" on:click|stopPropagation>#{number}</a>
  {#if score !== undefined}
    <small class="ticket-score" class:negative={score < 0} class:score-halo={mine && score > 0} title={scoreTitle(score, bucket)}>{scoreLabel(score)}</small>
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
  /* Gold, not the accent green: points are a reward, and the green is the
     interface's ordinary "this is a link / this is fine" colour. The same
     gold the leaderboard's medals already use.
     No halo by default. A gold glow behind gold text eats its own edge and
     the number reads soft; plain gold on the dark surface stays crisp. */
  .ticket-score {
    font-size: 11px;
    font-weight: 800;
    font-variant-numeric: tabular-nums;
    color: var(--amber);
    letter-spacing: 0.02em;
  }
  /* --bad-ink is the token the rest of the app uses for this. */
  .ticket-score.negative { color: var(--bad-ink); }

</style>
