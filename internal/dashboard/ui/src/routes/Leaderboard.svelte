<script lang="ts">
  import { onMount } from 'svelte';
  import { getLeaderboard } from '../lib/api';
  import { withFeed } from '../lib/feed';
  import { maxOf, signed } from '../lib/format';
  import { isViewer, viewer } from '../lib/viewer';
  import { approvalRate, barWidth, displayName, emptyReason, frozenNotice, meanScore, medal, netLines } from '../lib/leaderboard';
  import type { LeaderboardResponse } from '../lib/types';

  let days = 0;
  let data: LeaderboardResponse | null = null;

  $: entries = data?.entries ?? [];
  // Hoisted once per render rather than recomputed per row, matching how
  // Metrics and ActivityChart feed their scales.
  $: topScore = maxOf(entries, (e) => e.total, 0);
  $: empty = data ? emptyReason(data.enabled, entries, data.days) : '';
  $: frozen = frozenNotice(data?.mode);

  async function load() {
    data = await getLeaderboard(days, '');
    return 'leaderboard';
  }
  const reload = withFeed(load);
  function changed() { void reload(); }
  onMount(reload);
</script>

<section class="hero">
  <div>
    <p class="eyebrow">Author standings</p>
    <h1>Leaderboard</h1>
    <p>Points scale with how much was reviewed. Well-sized PRs, fewer review rounds, and removing code all pay a better rate.</p>
  </div>
  <div class="metrics-filters">
    <label>Range
      <select bind:value={days} on:change={changed}>
        <option value={0}>All time</option>
        <option value={7}>7 days</option>
        <option value={30}>30 days</option>
        <option value={90}>90 days</option>
      </select>
    </label>
  </div>
</section>

{#if frozen}
  <section class="panel"><p class="empty">{frozen}</p></section>
{/if}

{#if empty}
  <section class="panel"><p class="empty">{empty}</p></section>
{:else}
  <section class="panel board">
    <p class="board-head">
      <span>Rank</span><span>Author</span><span>Score</span><span>Reviews</span>
      <span>Approved</span><span>Mean</span><span>Net lines</span>
    </p>
    {#each entries as e (e.author)}
      {@const mine = isViewer(e.author, $viewer)}
      <p class="board-row" class:negative={e.total < 0} class:mine>
        <span class="rank">{medal(e.rank) || e.rank}</span>
        <span class="who">
          <strong>{displayName(e)}</strong>
          {#if e.name}<em>@{e.author}</em>{/if}
        </span>
        <span class="score">
          <span class="bar" style="--w: {barWidth(e.total, topScore)}%"></span>
          <b class="total" class:score-halo={mine && e.total > 0}>{e.total}</b>
        </span>
        <span>{e.reviews}</span>
        <span>{approvalRate(e)}%</span>
        <span>{meanScore(e)}</span>
        <span class="net" class:shrink={netLines(e) < 0}>{signed(netLines(e))}</span>
      </p>
    {/each}
  </section>
{/if}

<style>
  .board { display: grid; }
  .board p {
    margin: 0;
    display: grid;
    grid-template-columns: 56px minmax(160px, 2fr) minmax(120px, 1.4fr) repeat(4, 88px);
    gap: 12px;
    align-items: center;
    padding: 12px 20px;
    border-top: 1px solid var(--line);
    font-size: 13px;
  }
  .board-head {
    color: var(--faint);
    font-size: 11px !important;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }
  .rank { font-size: 16px; text-align: center; }
  .who { display: grid; gap: 1px; min-width: 0; }
  .who strong { font-size: 14px; }
  .who em { color: var(--faint); font-style: normal; font-size: 11px; }

  /* The bar sits behind the number rather than beside it, so the score stays
     readable at every width and a zero-width bar costs no column.
     
     The cell carries the EMPTY track and the padding. Padding is what keeps
     the digits off the fill's edge, and it does not move the bar with them:
     an absolutely positioned child is laid out against the padding box, so
     the track still spans the whole column while the number sits inside it. */
  .score {
    position: relative;
    display: flex;
    align-items: center;
    padding-inline: 10px;
    border-radius: 3px;
    background: color-mix(in srgb, var(--amber) 7%, transparent);
  }
  /* The bar is gold too, not the interface green. A gold number sitting on a
     green bar read as two unrelated things fighting; in one hue the bar is
     plainly the magnitude OF that number. Contrast comes from lightness (full
     gold text over a 20% wash) rather than from hue, so the glyphs stay hard
     against it. */
  .score .bar {
    position: absolute;
    inset: 0 auto 0 0;
    width: var(--w);
    background: color-mix(in srgb, var(--amber) 20%, transparent);
    border-radius: 3px;
  }
  /* The same gold as a history row's points, so "points" reads one way across
     the dashboard. Heavier than the row around it: at 15px regular the digits
     were the lightest thing in their own column. */
  .score b {
    position: relative;
    font-size: 15px;
    font-weight: 700;
    font-variant-numeric: tabular-nums;
    color: var(--amber);
  }
  /* The leaderboard total is larger than a history row's, so its halo is too. */
  .score b.score-halo { --halo-inset: -4px -9px; }
  /* A negative total has no bar to draw (barWidth floors at 0), so the track
     is all there is, and gold would be claiming points that were lost. */
  .board-row.negative .score { background: color-mix(in srgb, var(--bad-ink) 7%, transparent); }
  .board-row.negative .score b { color: var(--bad-ink); }

  .net { font-variant-numeric: tabular-nums; color: var(--dim); }
  /* Removing code is the good outcome, so it is the one that gets the accent.
     Strictly negative, not <= 0: a net of exactly zero is a pure move, or a PR
     whose every line was excluded as generated, and accenting those claims a
     reduction that did not happen. */
  .net.shrink { color: var(--accent); font-weight: 700; }

  .empty { margin: 0; padding: 24px 20px; color: var(--dim); }

  @media (max-width: 860px) {
    .board { overflow-x: auto; }
    .board p { min-width: 760px; }
  }
</style>
