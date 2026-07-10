<script lang="ts">
  import { maxOf } from './format';
  import type { Bucket } from './types';

  export let buckets: Bucket[] = [];

  $: chart = chartScale(buckets);

  // The chart's scale: total feeds the header caption, max is the bar-height
  // denominator. The bars themselves render inline from buckets.
  function chartScale(bs: Bucket[]) {
    const total = bs.reduce((n, b) => n + b.approved + b.commented + b.requested_changes, 0);
    const max = maxOf(bs, (b) => b.approved + b.commented + b.requested_changes);
    return { total, max };
  }
</script>

<section>
  <div class="section-head compact"><h2>Activity</h2><span>{chart.total ? `${chart.total} total · peak ${chart.max}/h` : '24h'}</span></div>
  {#if chart.total}
    <div class="bars" style={`--peak:${chart.max}`}>
      {#each buckets as b}
        <div title={`${new Date(b.hour).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })} - ${b.approved} approved, ${b.commented} commented, ${b.requested_changes} changes requested`}>
          <i class="approved" style={`height:${Math.max(2, (b.approved / chart.max) * 100)}%`}></i>
          <i class="commented" style={`height:${Math.max(2, (b.commented / chart.max) * 100)}%`}></i>
          <i class="changes" style={`height:${Math.max(2, (b.requested_changes / chart.max) * 100)}%`}></i>
        </div>
      {/each}
    </div>
    <div class="legend"><span><i class="approved"></i>Approved</span><span><i class="commented"></i>Commented</span><span><i class="changes"></i>Changes</span></div>
  {:else}
    <p class="muted">No reviews in the last 24h.</p>
  {/if}
</section>
