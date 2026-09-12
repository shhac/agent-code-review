<script lang="ts">
  import { simulateScoring } from './api';
  import ScoreCurve from './ScoreCurve.svelte';
  import { changedFrom, heatColor, ladderProblem, policyDoc, policyJSON, policyOf, rampCSS, sortTiers, type Policy } from './scoreshape';
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
  $: ladderNote = ladderProblem(policy.buckets);
  $: ordered = sim ? sim.probes.every((p, i) => i === 0 || sim!.probes[i - 1].score > p.score) : false;

  // Same contract as the calculator: the token is taken the moment the policy
  // changes, so an answer to the previous policy can never land under the new
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
      const got = await simulateScoring(policyDoc(policy), range, CELLS);
      if (mine !== asked) return;
      sim = got;
      error = '';
    } catch (e) {
      if (mine !== asked) return;
      // The last good survey stays on screen, dimmed. Half-typed numbers pass
      // through states the daemon will not resolve, and blanking the panel
      // every time takes away the picture being edited against.
      error = e instanceof Error ? e.message : String(e);
    }
  }

  // Committed, not on every keystroke: sorting as the digits arrive would send
  // a row intended for 150 to the top of the ladder the moment it read 1.
  function commitTiers() {
    policy = { ...policy, buckets: sortTiers(policy.buckets) };
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

  function addTier() {
    const tiers = policy.buckets;
    const prev = tiers.length > 1 ? tiers[tiers.length - 2].max_churn : 10;
    tiers.splice(tiers.length - 1, 0, {
      name: `tier${tiers.length}`,
      max_churn: Math.round(prev * 2) || 20,
      multiplier: tiers[tiers.length - 1].multiplier * 2,
    });
    policy = { ...policy, buckets: sortTiers(tiers) };
  }

  function dropTier(i: number) {
    policy = { ...policy, buckets: policy.buckets.filter((_, n) => n !== i) };
  }

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

  const dials: { key: keyof Policy; label: string; min: number; max: number; step: number; hint?: string }[] = [
    { key: 'churn_exponent', label: 'Size exponent', min: 0, max: 1, step: 0.05,
      hint: 'How much of a score follows sheer volume. At 1 a bigger PR always earns more. Below it, the same solve in fewer lines is worth more.' },
    { key: 'base', label: 'Base', min: 10, max: 600, step: 10, hint: 'Points for one full churn unit at 1x.' },
    { key: 'churn_unit', label: 'Churn unit', min: 10, max: 200, step: 5,
      hint: 'How many lines of churn the base pays for.' },
    { key: 'deletion_weight', label: 'Deletion weight', min: 0, max: 3, step: 0.1,
      hint: 'What a removed line counts, against an added one: churn = added + removed x this. Above 1 a removal counts for more, which moves a big deletion up the ladder into a worse rate.' },
    { key: 'shrink_bonus', label: 'Shrink bonus', min: 1, max: 2, step: 0.05, hint: 'Applied when the PR is net-negative.' },
    { key: 'attempt_decay', label: 'Revision decay', min: 0.05, max: 0.95, step: 0.05 },
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
        <output>{fmt(policy[d.key] as number, d.step < 0.1 ? 2 : d.step < 1 ? 2 : 0)}</output>
        <input
          id={`dial-${d.key}`}
          type="range"
          min={d.min}
          max={d.max}
          step={d.step}
          value={policy[d.key]}
          on:input={(e) => (policy = { ...policy, [d.key]: Number(e.currentTarget.value) })}
        />
        {#if d.hint}<p class="hint">{d.hint}</p>{/if}
      </div>
    {/each}

    <div class="dial">
      <label for="dial-curve">Curve</label>
      <output>
        <select id="dial-curve" bind:value={policy.curve}>
          <option value="linear">linear</option>
          <option value="step">step</option>
        </select>
      </output>
    </div>

    <table class="tier-edit">
      <thead><tr><th>Tier</th><th>Max churn</th><th>Rate</th><th></th></tr></thead>
      <tbody>
        {#each policy.buckets as b, i}
          <tr>
            <td><input id={`tier-name-${i}`} aria-label="tier name" bind:value={b.name} on:input={() => (policy = policy)} /></td>
            <td>
              {#if i === policy.buckets.length - 1}
                <!-- The catch-all has no bound to edit. Showing its stored 0
                     would read as a tier covering nothing. -->
                <input id={`tier-max-${i}`} aria-label="max churn" value="open" disabled />
              {:else}
                <input
                  id={`tier-max-${i}`}
                  aria-label="max churn"
                  type="number"
                  min="1"
                  bind:value={b.max_churn}
                  on:input={() => (policy = policy)}
                  on:change={commitTiers}
                />
              {/if}
            </td>
            <td><input id={`tier-rate-${i}`} aria-label="rate" type="number" step="0.05" bind:value={b.multiplier} on:input={() => (policy = policy)} /></td>
            <td class="x">
              {#if policy.buckets.length > 2}
                <button class="kill" title="Remove this tier" on:click={() => dropTier(i)}>&times;</button>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
    <button class="add-tier" on:click={addTier}>+ tier</button>
    {#if ladderNote}
      <p class="ladder-note">{ladderNote}</p>
    {/if}
    <p class="hint">
      Tiers sort themselves by max churn when you leave the field, so a new one
      lands where its bound puts it. The last row is the catch-all: it has no
      max churn, which is what <code>open</code> means, and only it may.
      To say a PR is too small to be worth points, give the first tier a
      <b>rate</b> of <code>0</code>.
    </p>
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
        <span class="probe-state" class:bad={!ordered}>{ordered ? 'tighter wins' : 'bigger wins'}</span>
      </div>

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
      <ScoreCurve buckets={policy.buckets} anchors={sim.anchors} curve={policy.curve} />

      <dl class="facts" class:shape-stale={error}>
        <div class="fact">
          <dt>Best-paid pull request</dt>
          <dd>+{sim.peak.lines} / -{sim.peak.lines} &rarr; {sim.peak.score} pts</dd>
          <p class="note">The balanced PR this policy pays most for: the shape it is asking people to write.</p>
        </div>
        <div class="fact">
          <dt>Size somebody would chop to</dt>
          <dd class:bad={sim.fragment.lines < 5}>{sim.fragment.lines} line{sim.fragment.lines === 1 ? '' : 's'}</dd>
          <p class="note">
            Where points per line peak, so this is the piece size that maximises a split. 2000 lines earn
            {sim.fragment.whole} shipped whole against {sim.fragment.split} split that way ({fmt(sim.fragment.gain, 1)}x).
            One line here means the ladder has no floor: give the first tier a <b>rate</b> of <code>0</code>.
          </p>
        </div>
        <div class="fact">
          <dt>Rate spread across the ladder</dt>
          <dd>{sim.rate_spread === null ? 'unbounded' : `${fmt(sim.rate_spread, 1)}x`}</dd>
          <p class="note">Best size-rate over worst. With the exponent at 1 this is the whole ceiling on what splitting can gain.</p>
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
