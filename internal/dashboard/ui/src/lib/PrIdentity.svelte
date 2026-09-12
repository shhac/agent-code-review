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
  // Whether these points are the viewer's own, which is the only thing that
  // animates. Seeing your own score move is the reward; seeing everybody
  // else's move is a page that will not sit still.
  export let mine = false;
</script>

<span class="ticket-id">
  <a class="ticket-pr" href={prHref(repo, number, url)} target="_blank" rel="noopener" on:click|stopPropagation>#{number}</a>
  {#if score !== undefined}
    <small class="ticket-score" class:negative={score < 0} class:mine={mine && score > 0} title={scoreTitle(score, bucket)}>{scoreLabel(score)}</small>
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
    position: relative;
    font-size: 11px;
    font-weight: 800;
    font-variant-numeric: tabular-nums;
    color: var(--amber);
    letter-spacing: 0.02em;
  }
  /* --bad-ink is the token the rest of the app uses for this. */
  .ticket-score.negative { color: var(--bad-ink); }

  /* Only the viewer's OWN points get a halo, and it pulses.
     Reserving it for one person's rows is what makes it affordable: on a
     typical page nothing is animating at all, rather than every row carrying
     an effect that has to be composited whether or not anyone cares about it.
     The halo is PALE, not gold: a different value from the text leaves the
     glyph edges hard, so the number stays legible while the surround moves.
     Animating the pseudo-element's opacity rather than a text-shadow keeps it
     on the compositor, so the text itself is never repainted. */
  .ticket-score.mine::before {
    content: '';
    position: absolute;
    inset: -3px -7px;
    border-radius: 999px;
    background: radial-gradient(ellipse at center, rgba(255, 252, 240, 0.5), transparent 70%);
    opacity: 0.2;
    animation: score-pulse 3.2s ease-in-out infinite;
    pointer-events: none;
  }
  @keyframes score-pulse {
    0%, 100% { opacity: 0.12; }
    50% { opacity: 0.42; }
  }
  /* Anyone who has asked for less motion, and every device that reports it,
     keeps the halo and loses the movement. */
  @media (prefers-reduced-motion: reduce) {
    .ticket-score.mine::before { animation: none; opacity: 0.28; }
  }
</style>
