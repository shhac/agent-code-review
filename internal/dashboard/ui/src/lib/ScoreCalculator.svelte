<script lang="ts">
  import { getScorePreview } from './api';
  import type { ScorePreview } from './types';

  // Every figure here comes back from /api/score/preview: the browser asks the
  // scorer what a PR is worth rather than working it out, so a number shown to
  // somebody planning their next PR is the number they will actually be paid.

  const VERDICTS: [string, string][] = [
    ['APPROVED', 'approved'],
    ['COMMENTED', 'commented'],
    ['REQUESTED_CHANGES', 'changes requested'],
  ];
  const label = (v: string) => VERDICTS.find(([value]) => value === v)?.[1] ?? v.toLowerCase();

  let additions = 200;
  let deletions = 100;
  let rounds = 1;
  let earlier = 'COMMENTED';
  let final = 'APPROVED';

  let preview: ScorePreview | null = null;
  let error = '';

  // The sequence the rounds selector stands for: the earlier rounds are
  // feedback, the last one is the outcome. Keeping it derived means the
  // request and the on-screen explanation cannot disagree.
  $: verdicts = [...Array(Math.max(rounds - 1, 0)).fill(earlier), final];

  // Debounced, because these are number inputs and every keystroke is a new
  // hypothesis. A stale reply must never overwrite a fresh one, so each run
  // checks it is still the latest before assigning.
  let timer: ReturnType<typeof setTimeout> | undefined;
  let latest = 0;
  $: schedule(additions, deletions, verdicts.join(','));

  function schedule(..._deps: unknown[]) {
    clearTimeout(timer);
    timer = setTimeout(run, 150);
  }

  async function run() {
    const seq = verdicts;
    const mine = ++latest;
    try {
      const got = await getScorePreview({ additions, deletions, verdicts: seq });
      if (mine !== latest) return;
      preview = got;
      error = '';
    } catch (e) {
      if (mine !== latest) return;
      error = e instanceof Error ? e.message : String(e);
    }
  }

  // A negative total is a real outcome (changes requested costs points), so
  // the sign is shown rather than implied by a colour alone.
  const signed = (n: number) => (n > 0 ? `+${n}` : String(n));
</script>

<div class="calc">
  <div class="calc-inputs">
    <label>
      <span>Lines added</span>
      <input type="number" min="0" max="1000000" bind:value={additions} />
    </label>
    <label>
      <span>Lines removed</span>
      <input type="number" min="0" max="1000000" bind:value={deletions} />
    </label>
    <label>
      <span>Review rounds</span>
      <select bind:value={rounds}>
        {#each [1, 2, 3, 4, 5] as n}<option value={n}>{n}</option>{/each}
      </select>
    </label>
    {#if rounds > 1}
      <label>
        <span>Earlier rounds</span>
        <select bind:value={earlier}>
          {#each VERDICTS.slice(1) as [value, text]}<option {value}>{text}</option>{/each}
        </select>
      </label>
    {/if}
    <label>
      <span>Final verdict</span>
      <select bind:value={final}>
        {#each VERDICTS as [value, text]}<option {value}>{text}</option>{/each}
      </select>
    </label>
  </div>

  {#if error}
    <p class="calc-error">{error}</p>
  {:else if preview}
    <div class="calc-out">
      <p class="calc-total" class:bad={preview.total < 0}>
        {signed(preview.total)}<em>points</em>
      </p>
      <p class="calc-why">
        <b>{preview.churn}</b> churn ({preview.additions} added, {preview.deletions} removed at the configured weight)
        lands in <b>{preview.bucket}</b>, paid at <b>{preview.rate.toFixed(2)}x</b>.
      </p>
      {#if preview.rounds.length > 1}
        <ol class="calc-rounds">
          {#each preview.rounds as r}
            <li><span>Round {r.attempt}</span><span class="muted">{label(r.verdict)}</span><b class:bad={r.score < 0}>{signed(r.score)}</b></li>
          {/each}
        </ol>
      {/if}
    </div>
  {/if}
</div>

<style>
  .calc { padding: 4px 20px 20px; display: grid; gap: 16px; }
  .calc-inputs { display: flex; flex-wrap: wrap; gap: 12px; }
  .calc-inputs label { display: grid; gap: 5px; }
  .calc-inputs span {
    color: var(--faint); font-size: 11px; font-weight: 800;
    letter-spacing: .08em; text-transform: uppercase;
  }
  .calc-inputs input, .calc-inputs select {
    padding: 8px 10px; min-width: 132px; border-radius: 8px;
    border: 1px solid var(--line); background: var(--surface-warm); color: var(--ink);
  }
  .calc-out { display: grid; gap: 8px; }
  .calc-total { margin: 0; font-size: 34px; line-height: 1; font-weight: 800; color: var(--accent); }
  .calc-total.bad { color: var(--bad-ink); }
  .calc-total em { margin-left: 8px; font-size: 13px; font-style: normal; font-weight: 600; color: var(--dim); }
  .calc-why { margin: 0; color: var(--dim); font-size: 13px; }
  .calc-why b { color: var(--ink); font-weight: 600; }
  .calc-error { margin: 0; color: var(--bad-ink); font-size: 13px; }
  .calc-rounds { margin: 4px 0 0; padding: 0; list-style: none; display: grid; gap: 4px; max-width: 340px; }
  .calc-rounds li {
    display: grid; grid-template-columns: 1fr 1fr auto; gap: 12px;
    padding: 6px 10px; border: 1px solid var(--line); border-radius: 8px; background: var(--surface-warm);
  }
  .calc-rounds b { color: var(--accent); }
  .calc-rounds b.bad { color: var(--bad-ink); }
</style>
