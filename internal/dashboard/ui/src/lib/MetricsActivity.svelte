<script lang="ts">
  // The Metrics page's daily panel: completed reviews as bars, tokens
  // processed as a trend line over them, each on its own axis.
  import { maxOf, tokens } from './format';
  import { barHeight, trendPoints } from './metrics';
  import type { MetricsResponse } from './types';

  export let activity: MetricsResponse['activity'];

  $: maxReviews = maxOf(activity, (d) => d.reviews);
  $: maxTokens = maxOf(activity, (d) => d.fresh_tokens);
  $: tokenPoints = trendPoints(activity, maxTokens);
</script>

<!-- Every line break below falls between the children of a flex or grid
     container, or between absolutely positioned elements, so none of it can
     render as a space. -->
<section class="surface metric-panel activity-panel">
  <div class="section-head"><h2>Completed reviews + tokens processed</h2><span>daily</span></div>
  <div class="activity-plot">
    <span class="activity-axis left title">reviews</span>
    <span class="activity-axis left top">{maxReviews}</span>
    <span class="activity-axis left bottom">0</span>
    <span class="activity-axis right title">tokens</span>
    <span class="activity-axis right top">{tokens(maxTokens) || '0'}</span>
    <span class="activity-axis right bottom">0</span>
    {#each activity as day}
      <div class="activity-day" title={`${day.day}: ${day.reviews} reviews · ${day.fresh_tokens} tokens`}>
        <i class="review-bar" style={`height:${barHeight(day.reviews, maxReviews)}%`}></i>
      </div>
    {/each}
    <svg class="token-trend" viewBox="0 0 100 100" preserveAspectRatio="none" aria-label="Token spend trend">
      <polyline points={tokenPoints} />
    </svg>
  </div>
  <div class="legend">
    <span><i class="approved"></i>completed reviews</span>
    <span><i class="commented"></i>tokens used</span>
  </div>
</section>
