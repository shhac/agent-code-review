<script lang="ts">
  import { onMount } from 'svelte';
  import { getLeaderboard } from '../lib/api';
  import { withFeed } from '../lib/feed';
  import { maxOf, signed } from '../lib/format';
  import { isViewer, viewer } from '../lib/viewer';
  import { barWidth, columnFor, columns, displayName, emptyReason, frozenNotice, isThinSample, medal } from '../lib/leaderboard';
  import type { LeaderboardResponse, LeaderSort } from '../lib/types';

  let days = 0;
  // The sort is sent to the daemon rather than applied here, because rank and
  // the row limit both depend on it: re-sorting a top-100-by-total in the
  // browser would show the hundred biggest contributors arranged by median,
  // which is a different set of people from the hundred best medians.
  let sort: LeaderSort = 'total';
  let data: LeaderboardResponse | null = null;

  $: entries = data?.entries ?? [];
  $: ranked = columnFor(sort);
  // Hoisted once per render rather than recomputed per row, matching how
  // Metrics and ActivityChart feed their scales. Scaled on the RANKED column,
  // so the bar is always describing the thing the board is ordered by.
  $: topScore = maxOf(entries, ranked.value, 0);
  $: empty = data ? emptyReason(data.enabled, entries, data.days) : '';
  $: frozen = frozenNotice(data?.mode);

  async function load() {
    data = await getLeaderboard(days, '', sort);
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
    <p>Well-sized PRs, fewer review rounds, and removing code all pay better. Rank by any column: totals reward volume, mean and median describe a typical pull request.</p>
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
      <span>Rank</span><span>Author</span>
      {#each columns as c}
        <!-- Toggle buttons, not ARIA columnheaders: this board is a CSS grid
             of paragraphs, and claiming table semantics for one row of it
             would promise a structure the rest of the markup does not have.
             aria-pressed says the true thing, that this is the measure the
             board is currently ranked by. -->
        <span>
          <button
            class="sort"
            class:on={sort === c.sort}
            aria-pressed={sort === c.sort}
            title={`Rank by ${c.label.toLowerCase()}`}
            on:click={() => { sort = c.sort; changed(); }}
          >
            {c.label}{#if sort === c.sort}<em aria-hidden="true">&#9662;</em>{/if}
          </button>
        </span>
      {/each}
    </p>
    {#each entries as e (e.author)}
      {@const mine = isViewer(e.author, $viewer)}
      <p class="board-row" class:negative={e.total < 0} class:mine>
        <span class="rank">{medal(e.rank) || e.rank}</span>
        <span class="who">
          <strong>{displayName(e)}</strong>
          {#if e.name}<em>@{e.author}</em>{/if}
        </span>
        {#each columns as c}
          {@const v = c.value(e)}
          {#if c.sort === sort}
            <!-- The ranked column carries the bar and the emphasis: the score
                 is whichever number this board is ordered by. -->
            <span class="score" class:no-bar={!c.bar} class:thin={isThinSample(e, sort)}>
              {#if c.bar}<span class="bar" style="--w: {barWidth(v, topScore)}%"></span>{/if}
              <b class="total" class:score-halo={mine && v > 0}>{c.sort === 'approved' ? `${v}%` : c.sort === 'net' ? signed(v) : v}</b>
            </span>
          {:else}
            <span class="stat" class:shrink={c.sort === 'net' && v < 0}>
              {c.sort === 'approved' ? `${v}%` : c.sort === 'net' ? signed(v) : v}
            </span>
          {/if}
        {/each}
      </p>
    {/each}
  </section>
{/if}

<style>
  .board { display: grid; }
  .board p {
    margin: 0;
    display: grid;
    /* Six measures now, and the ranked one holds a bar, so it takes the wide
       track. Every column is the same width whichever is ranked: a table whose
       columns resize when you re-sort it is a table nobody can scan. */
    grid-template-columns: 56px minmax(140px, 1.6fr) repeat(6, minmax(86px, 1fr));
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
  /* A header that re-ranks the board has to look like it does something, but
     it is a label first: the base button rule is a filled pill, so its ground,
     radius and hover lift are all reset. */
  .sort {
    padding: 0; border: 0; border-radius: 0; background: none; cursor: pointer;
    font: inherit; font-size: 11px; letter-spacing: .08em; text-transform: uppercase;
    color: var(--faint);
  }
  .sort:hover { color: var(--ink); transform: none; }
  .sort.on { color: var(--amber); }
  .sort em { font-style: normal; margin-left: 3px; }
  .stat { font-variant-numeric: tabular-nums; color: var(--dim); }
  .stat.shrink { color: var(--accent); font-weight: 700; }
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

  /* A per-review measure over one or two reviews is a single PR wearing a
     trend's clothes. The row still ranks: dropping somebody off a board they
     are on is worse than a number with a caveat, and the review count is in
     its own column. It is dimmed instead, which is how this dashboard already
     draws a value that is inherited rather than chosen. */
  .score.thin { opacity: .5; }
  /* Removing code is the good outcome, so it is the one that gets the accent.
     Strictly negative, not <= 0: a net of exactly zero is a pure move, or a PR
     whose every line was excluded as generated, and accenting those claims a
     reduction that did not happen. */
  .score.no-bar b, .stat.shrink { font-weight: 700; }

  .empty { margin: 0; padding: 24px 20px; color: var(--dim); }

  @media (max-width: 860px) {
    .board { overflow-x: auto; }
    .board p { min-width: 760px; }
  }
</style>
