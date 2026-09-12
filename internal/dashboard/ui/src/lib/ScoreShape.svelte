<script lang="ts">
  import { simulateScoring } from './api';
  import ScoreRewardCurve from './ScoreRewardCurve.svelte';
  import { changedFrom, heatColor, policyJSON, policyOf, rampCSS, tierRanges, type Policy } from './scoreshape';
  import type { ConfigResponse, ScoreSimulation } from './types';

  export let config: ConfigResponse;

  // The daemon's live policy is both the starting point and the thing a draft
  // is measured against, so it is kept apart from the one being edited.
  const live: Policy = policyOf(config);
  let policy: Policy = policyOf(config);
  let range = 400;
  const CELLS = 48;

  let sim: ScoreSimulation | null = null;
  let error = '';

  $: changed = changedFrom(policy, live);

  // The token is taken the moment the policy changes, not when the request
  // goes out, so an answer to the previous policy can never land under the new
  // dials.
  let timer: ReturnType<typeof setTimeout> | undefined;
  let asked = 0;
  $: ask(JSON.stringify(policy), range);

  function ask(..._inputs: unknown[]) {
    const mine = ++asked;
    clearTimeout(timer);
    timer = setTimeout(() => run(mine), 180);
  }

  async function run(mine: number) {
    try {
      const got = await simulateScoring(policy, range, CELLS);
      if (mine !== asked) return;
      sim = got;
      error = '';
    } catch (e) {
      if (mine !== asked) return;
      // The last good survey stays on screen, dimmed: half-typed numbers pass
      // through states the daemon will not resolve, and blanking the panel
      // takes away the picture being edited against.
      error = e instanceof Error ? e.message : String(e);
    }
  }

  // Painting is the only thing this panel does with the numbers. Every one of
  // them was computed by the daemon's scorer.
  let canvases: Record<number, HTMLCanvasElement | undefined> = {};
  $: if (sim) for (const g of sim.grids) paint(canvases[g.rounds], g.cells, sim.max);

  function paint(canvas: HTMLCanvasElement | undefined, cells: number[][], max: number) {
    const ctx = canvas?.getContext('2d');
    if (!canvas || !ctx) return;
    const size = canvas.width;
    const cell = size / cells.length;
    ctx.clearRect(0, 0, size, size);
    for (let y = 0; y < cells.length; y++) {
      for (let x = 0; x < cells[y].length; x++) {
        const v = cells[y][x];
        const [r, g, b] = heatColor(max > 0 ? v / max : 0, v < 0);
        ctx.fillStyle = `rgb(${r},${g},${b})`;
        // Row 0 is the fewest removed lines, and removals grow up the page.
        ctx.fillRect(x * cell, size - (y + 1) * cell, Math.ceil(cell), Math.ceil(cell));
      }
    }
    // The balanced diagonal: every PR that neither grows nor shrinks the tree.
    ctx.strokeStyle = 'rgba(240,240,240,0.26)';
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(0, size);
    ctx.lineTo(size, 0);
    ctx.stroke();
  }

  const marks = (n: number) => [0, n / 4, n / 2, (n * 3) / 4, n].map(Math.round);
  const fmt = (n: number, d = 2) => n.toFixed(d);

  let copied = false;
  async function copyJSON() {
    try {
      await navigator.clipboard.writeText(policyJSON(policy));
      copied = true;
      setTimeout(() => (copied = false), 1600);
    } catch {
      copied = false;
    }
  }

  // Four dials, each phrased as the question it answers. The old ruleset had
  // eight and a ladder, and the reason this panel needed heatmaps to be
  // understood at all was that no single dial meant anything on its own.
  const dials: { key: keyof Policy; label: string; min: number; max: number; step: number; hint: string }[] = [
    { key: 'piece_lines', label: 'Best piece size', min: 5, max: 400, step: 5,
      hint: 'Changed lines that earn the most points PER LINE: the size to aim for when splitting a large change into a stack.' },
    { key: 'size_points', label: 'Points at the peak', min: 10, max: 600, step: 10,
      hint: 'The most a single PR can earn for its size. Sets the scale of the board and nothing else.' },
    { key: 'size_falloff', label: 'Size falloff', min: 2.1, max: 8, step: 0.1,
      hint: 'How sharply a PR stops being worth more as it grows. Higher widens the gap between a stack of well-sized PRs and one enormous one.' },
    { key: 'removal_points_per_100', label: 'Removal, per 100 lines', min: 0, max: 100, step: 5,
      hint: 'Points for a hundred NET removed lines, paid on top of the size reward. Set it to 0 to stop paying for deletions.' },
  ];
</script>

<div class="shape">
  <div class="shape-controls">
    <div class="shape-head">
      <span class="shape-state" class:draft={changed.length}>
        {changed.length ? `draft · ${changed.join(', ')}` : 'the policy this daemon is running'}
      </span>
      {#if changed.length}
        <button class="linkish" on:click={() => (policy = policyOf(config))}>reset</button>
      {/if}
    </div>

    {#each dials as d}
      <div class="dial">
        <label for={`dial-${d.key}`}>{d.label}</label>
        <output>{fmt(policy[d.key], d.step < 1 ? 1 : 0)}</output>
        <input
          id={`dial-${d.key}`}
          type="range"
          min={d.min}
          max={d.max}
          step={d.step}
          value={policy[d.key]}
          on:input={(e) => (policy = { ...policy, [d.key]: Number(e.currentTarget.value) })}
        />
        <p class="hint">{d.hint}</p>
      </div>
    {/each}

    {#if sim}
      <table class="tier-table">
        <thead><tr><th>Tier</th><th>Changed lines</th></tr></thead>
        <tbody>
          {#each tierRanges(sim.tiers) as t}
            <tr><td>{t.name}</td><td class="mono">{t.range}</td></tr>
          {/each}
        </tbody>
      </table>
      <p class="hint">Labels, not dials. They follow the two sizes above, so a score always explains itself against the policy you set.</p>
    {/if}
  </div>

  <div class="shape-output">
    {#if error}
      <p class="shape-error">{error}</p>
    {/if}
    {#if sim}
      <div class="probe-row" class:shape-stale={error}>
        {#each sim.probes as p, i}
          {#if i > 0}<span class="probe-arrow">{sim.probes[i - 1].score > p.score ? '>' : '<'}</span>{/if}
          <div class="probe" class:best={p.score === Math.max(...sim.probes.map((q) => q.score))}>
            <b>{p.score}</b><span>+{p.lines} / -{p.lines}</span>
          </div>
        {/each}
        <span class="probe-state" class:bad={!(sim.probes[0].score > sim.probes[1].score && sim.probes[1].score > sim.probes[2].score)}>
          {sim.probes[0].score > sim.probes[1].score && sim.probes[1].score > sim.probes[2].score ? 'tighter wins' : 'bigger wins'}
        </span>
      </div>

      <ScoreRewardCurve
        curve={sim.curve}
        peak={sim.peak}
        pieceLines={policy.piece_lines}
        sizePoints={policy.size_points}
      />

      <div class="shape-ranges">
        {#each [200, 400, 1000, 2000] as r}
          <button class:on={range === r} on:click={() => (range = r)}>0&ndash;{r} lines</button>
        {/each}
      </div>

      <div class="maps" class:shape-stale={error}>
        {#each sim.grids as g}
          <div class="map">
            <h4>{g.rounds === 1 ? 'Approved first pass' : 'Comment, then approve'} <em>{g.rounds} round{g.rounds > 1 ? 's' : ''}</em></h4>
            <div class="plot">
              <span class="axis-label y">lines removed</span>
              <div class="yaxis">{#each marks(range).slice().reverse() as m}<span>{m}</span>{/each}</div>
              <canvas width="384" height="384" bind:this={canvases[g.rounds]}></canvas>
              <div class="xaxis">{#each marks(range) as m}<span>{m}</span>{/each}</div>
              <span class="axis-label x">lines added</span>
            </div>
          </div>
        {/each}
      </div>
      <div class="ramp">
        <div class="ramp-bar" style={`background:${rampCSS()}`}></div>
        <div class="ramp-ends"><span>0 pts</span><span>{sim.max} pts</span></div>
      </div>

      <dl class="facts" class:shape-stale={error}>
        <div class="fact">
          <dt>Best-paid pull request</dt>
          <dd>+{sim.best_pr.lines} / -{sim.best_pr.lines} &rarr; {sim.best_pr.score} pts</dd>
          <p class="note">The balanced PR this policy pays most for: the shape it is asking people to write.</p>
        </div>
        <div class="fact">
          <dt>Splitting 2000 lines</dt>
          <dd>{fmt(sim.fragment.gain, 1)}x</dd>
          <p class="note">
            {sim.fragment.whole} pts shipped whole against {sim.fragment.split} as pieces of
            {sim.fragment.lines} lines. This premium is the loudest thing the policy says; the falloff dial sets it.
          </p>
        </div>
      </dl>

      <div class="json-head">
        <span>Paste into <code>config.json</code>, or apply it with <code>agent-code-review config set</code></span>
        <button on:click={copyJSON}>{copied ? 'copied' : 'copy'}</button>
      </div>
      <pre class="policy-json">{policyJSON(policy)}</pre>
    {:else if !error}
      <p class="hint">Surveying the policy&hellip;</p>
    {/if}
  </div>
</div>
