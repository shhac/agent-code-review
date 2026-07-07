<script lang="ts">
  type Candidate = {
    repo: string;
    number: number;
    url?: string;
    title: string;
    type: string;
    author: string;
    status: string;
    head_sha: string;
    queue_pos: number;
    created_at: string;
    updated_at: string;
    discovered_at: string;
  };

  type Review = {
    repo: string;
    number: number;
    verdict: string;
    engine: string;
    head_sha: string;
    reviewed_at: string;
  };

  type Run = {
    started_at: string;
    finished_at: string;
    status: string;
    host: string;
  };

  type Bucket = {
    hour: string;
    approved: number;
    commented: number;
    requested_changes: number;
  };

  type UsageWindow = {
    window_mins: number;
    used_percent: number;
    resets_at?: number;
  };

  type UsageSnapshot = {
    error?: string;
    plan?: string;
    fetched_at?: string;
    primary?: UsageWindow;
    secondary?: UsageWindow;
  };

  type Feed = { ok: boolean; detail: string };
  type Route = 'overview' | 'config' | 'prompt' | 'logs';

  const nav: { route: Route; label: string; path: string }[] = [
    { route: 'overview', label: 'Queue', path: '/' },
    { route: 'config', label: 'Config', path: '/config' },
    { route: 'prompt', label: 'Prompt', path: '/prompt' },
    { route: 'logs', label: 'Logs', path: '/logs' },
  ];

  let route: Route = routeFromPath(location.pathname);
  let feed: Feed = { ok: true, detail: 'syncing' };
  let queue: Candidate[] = [];
  let reviews: Review[] = [];
  let runs: Run[] = [];
  let buckets: Bucket[] = [];
  let usageAvailable = false;
  let usage: UsageSnapshot | null = null;
  let addInput = '';
  let addErr = '';
  let queueShowAll = false;
  let reviewLimit = 50;
  let expanded = new Set<string>();
  let overviewTimer: number | undefined;
  let logsTimer: number | undefined;

  let configData: any = null;
  let authors: any[] = [];
  let promptData: any = null;
  let previewVariant = 'allowed_author';
  let logsAvailable = true;
  let logEntries: any[] = [];
  let logPane: HTMLDivElement;

  $: queued = queue.filter((c) => c.status === 'queued').length;
  $: reviewing = queue.filter((c) => c.status === 'reviewing').length;
  $: attention = queue.length - queued - reviewing;
  $: totalReviews = sumBuckets('approved') + sumBuckets('commented') + sumBuckets('requested_changes');
  $: approvedReviews = sumBuckets('approved');
  $: approvalRate = totalReviews ? Math.round((approvedReviews / totalReviews) * 100) : 0;
  $: visibleQueue = queueShowAll ? queue : queue.slice(0, 100);
  $: lastRun = runs[0];
  $: activePreview = promptData?.previews?.[previewVariant] || '';
  $: outcomes = Object.entries(promptData?.outcomes || {}).filter(([, v]) => v);

  function routeFromPath(path: string): Route {
    if (path === '/config' || path === '/config.html') return 'config';
    if (path === '/prompt' || path === '/prompt.html') return 'prompt';
    if (path === '/logs' || path === '/logs.html') return 'logs';
    return 'overview';
  }

  function go(path: string) {
    history.pushState({}, '', path);
    route = routeFromPath(location.pathname);
    loadRoute();
  }

  function esc(s: unknown) {
    return String(s ?? '');
  }

  function when(t: string) {
    return t ? new Date(t).toLocaleString() : '';
  }

  function rel(t: string) {
    if (!t || new Date(t).getFullYear() < 2000) return '';
    const s = Math.max(0, (Date.now() - new Date(t).getTime()) / 1000);
    if (s < 60) return `${Math.floor(s)}s`;
    if (s < 3600) return `${Math.floor(s / 60)}m`;
    if (s < 86400) return `${Math.floor(s / 3600)}h`;
    return `${Math.floor(s / 86400)}d`;
  }

  function ago(t: string) {
    const r = rel(t);
    return r ? `${r} ago` : '';
  }

  function dur(a: string, b: string) {
    if (!a || !b) return '';
    const s = Math.max(0, (new Date(b).getTime() - new Date(a).getTime()) / 1000);
    if (s < 90) return `${Math.round(s)}s`;
    if (s < 5400) return `${Math.round(s / 60)}m`;
    return `${(s / 3600).toFixed(1)}h`;
  }

  function keyOf(c: Candidate) {
    return `${c.repo}#${c.number}`;
  }

  function prHref(repo: string, number: number, url?: string) {
    return url || `https://github.com/${repo}/pull/${number}`;
  }

  function statusLabel(s: string) {
    return esc(s).replace(/_/g, ' ');
  }

  function statusKind(s: string) {
    const kinds: Record<string, string> = {
      queued: 'dim',
      reviewing: 'info live',
      reviewed: 'ok',
      skipped: 'warn',
      error: 'bad',
      APPROVED: 'ok',
      COMMENTED: 'info',
      REQUESTED_CHANGES: 'bad',
      SKIPPED: 'warn',
      ERROR: 'bad',
      running: 'info live',
      done: 'ok',
      failed: 'bad',
      off: 'dim',
    };
    return kinds[s] || 'dim';
  }

  function sumBuckets(k: keyof Pick<Bucket, 'approved' | 'commented' | 'requested_changes'>) {
    return buckets.reduce((n, b) => n + b[k], 0);
  }

  async function fetchJSON(path: string) {
    const res = await fetch(path);
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || res.statusText);
    return data;
  }

  async function post(path: string, body: unknown) {
    const res = await fetch(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (!res.ok) throw new Error((await res.json()).error || res.statusText);
  }

  async function refreshOverview() {
    try {
      const [q, rv, rn, us, st] = await Promise.all([
        fetchJSON('/api/queue'),
        fetchJSON(`/api/reviews?limit=${reviewLimit}`),
        fetchJSON('/api/runs'),
        fetchJSON('/api/usage'),
        fetchJSON('/api/stats'),
      ]);
      queue = q.candidates || [];
      reviews = rv.reviews || [];
      runs = rn.runs || [];
      usageAvailable = !!us.available;
      usage = us.usage || null;
      buckets = st.buckets || [];
      feed = { ok: true, detail: new Date().toLocaleTimeString() };
    } catch {
      feed = { ok: false, detail: 'stale' };
    }
  }

  async function addToQueue() {
    addErr = '';
    try {
      await post('/api/queue', { url: addInput.trim() });
      addInput = '';
      await refreshOverview();
    } catch (e: any) {
      addErr = e.message;
    }
  }

  async function removeCandidate(c: Candidate) {
    addErr = '';
    try {
      const res = await fetch('/api/queue', {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ repo: c.repo, number: c.number }),
      });
      if (!res.ok) throw new Error((await res.json()).error || res.statusText);
      await refreshOverview();
    } catch (e: any) {
      addErr = e.message;
    }
  }

  async function moveCandidate(c: Candidate, direction: 'up' | 'down') {
    addErr = '';
    try {
      await post('/api/queue/move', { repo: c.repo, number: c.number, direction });
      await refreshOverview();
    } catch (e: any) {
      addErr = e.message;
    }
  }

  function toggleCandidate(c: Candidate) {
    const next = new Set(expanded);
    const k = keyOf(c);
    if (next.has(k)) next.delete(k);
    else next.add(k);
    expanded = next;
  }

  function historyFor(c: Candidate) {
    return reviews.filter((r) => r.repo === c.repo && r.number === c.number);
  }

  function windowName(w: UsageWindow | undefined, fallback: string) {
    if (!w) return fallback;
    if (w.window_mins >= 10080) return 'Weekly';
    if (w.window_mins >= 60) return `${Math.round(w.window_mins / 60)}h window`;
    return `${w.window_mins}m window`;
  }

  function chartBars() {
    const total = buckets.reduce((n, b) => n + b.approved + b.commented + b.requested_changes, 0);
    const max = Math.max(1, ...buckets.map((b) => b.approved + b.commented + b.requested_changes));
    return { total, max };
  }

  async function loadConfig() {
    try {
      const [cfg, au] = await Promise.all([fetchJSON('/api/config'), fetchJSON('/api/authors')]);
      configData = cfg;
      authors = au.authors || [];
      feed = { ok: true, detail: 'read-only' };
    } catch {
      feed = { ok: false, detail: 'stale' };
    }
  }

  async function loadPrompt() {
    try {
      promptData = await fetchJSON('/api/prompt');
      feed = { ok: true, detail: 'read-only' };
    } catch {
      feed = { ok: false, detail: 'stale' };
    }
  }

  async function refreshLogs() {
    try {
      const pinned = logPane ? logPane.scrollHeight - logPane.scrollTop - logPane.clientHeight < 40 : true;
      const data = await fetchJSON('/api/logs');
      logsAvailable = !!data.available;
      logEntries = data.entries || [];
      feed = { ok: true, detail: `${logEntries.length} lines` };
      setTimeout(() => {
        if (pinned && logPane) logPane.scrollTop = logPane.scrollHeight;
      });
    } catch {
      feed = { ok: false, detail: 'stale' };
    }
  }

  function clearTimers() {
    if (overviewTimer) window.clearInterval(overviewTimer);
    if (logsTimer) window.clearInterval(logsTimer);
    overviewTimer = undefined;
    logsTimer = undefined;
  }

  function loadRoute() {
    clearTimers();
    if (route === 'overview') {
      refreshOverview();
      overviewTimer = window.setInterval(refreshOverview, 15000);
    } else if (route === 'config') {
      loadConfig();
    } else if (route === 'prompt') {
      loadPrompt();
    } else if (route === 'logs') {
      refreshLogs();
      logsTimer = window.setInterval(refreshLogs, 5000);
    }
  }

  window.addEventListener('popstate', () => {
    route = routeFromPath(location.pathname);
    loadRoute();
  });

  loadRoute();
</script>

<svelte:head>
  <title>agent-code-review · {nav.find((n) => n.route === route)?.label}</title>
</svelte:head>

<div class="shell">
  <aside class="rail">
    <button class="brand" type="button" on:click={() => go('/')}>
      <img src="/mascot.webp" alt="agent-code-review mascot" width="64" height="64" />
      <span>
        <strong>agent</strong>
        <em>code review</em>
      </span>
    </button>
    <nav aria-label="Dashboard">
      {#each nav as item}
        <a href={item.path} class:active={route === item.route} on:click|preventDefault={() => go(item.path)}>{item.label}</a>
      {/each}
    </nav>
    <div class:stale={!feed.ok} class="feed">
      <span class="signal"></span>
      <span>{feed.ok ? 'live' : 'stale'}</span>
      <small>{feed.detail}</small>
    </div>
  </aside>

  <main>
    {#if route === 'overview'}
      <section class="hero">
        <div>
          <p class="eyebrow">Review dispatch</p>
          <h1>{queued} queued, {reviewing} underway</h1>
          <p>{totalReviews} reviews in the last 24h · {totalReviews ? `${approvalRate}% approved` : 'no outcomes yet'}{attention ? ` · ${attention} need attention` : ''}</p>
        </div>
        <form class="add" on:submit|preventDefault={addToQueue}>
          <input bind:value={addInput} placeholder="owner/repo/pull/123 or GitHub PR URL" required />
          <button type="submit">Queue PR</button>
          {#if addErr}<span class="err">{addErr}</span>{/if}
        </form>
      </section>

      <div class="overview">
        <section class="queue-board">
          <div class="section-head">
            <div>
              <p class="eyebrow">Worklist</p>
              <h2>Pull requests</h2>
            </div>
            <span>{queue.length ? `${queued} queued · ${reviewing} reviewing${attention ? ` · ${attention} attention` : ''}` : 'empty'}</span>
          </div>

          {#if visibleQueue.length}
            <div class="queue-list">
              {#each visibleQueue as c}
                <article class:open={expanded.has(keyOf(c))} class="ticket">
                  <button class="ticket-main" type="button" on:click={() => toggleCandidate(c)}>
                    <span class="chev">{expanded.has(keyOf(c)) ? '⌄' : '›'}</span>
                    <span class="ticket-pr">#{c.number}</span>
                    <span class="ticket-copy">
                      <strong>{c.title}</strong>
                      <small>{c.repo} · @{c.author}</small>
                    </span>
                    <span class="tag">{c.type}</span>
                    <span class="status {statusKind(c.status)}"><i></i>{statusLabel(c.status)}</span>
                  </button>
                  <div class="ticket-actions">
                    {#if c.status === 'queued'}
                      <button aria-label="Move up" title="Move up" on:click={() => moveCandidate(c, 'up')}>↑</button>
                      <button aria-label="Move down" title="Move down" on:click={() => moveCandidate(c, 'down')}>↓</button>
                    {/if}
                    <a href={prHref(c.repo, c.number, c.url)} target="_blank" rel="noopener">Open</a>
                    <button class="danger" aria-label="Remove from queue" title="Remove from queue" on:click={() => removeCandidate(c)}>×</button>
                  </div>
                  {#if expanded.has(keyOf(c))}
                    <div class="ticket-detail">
                      <dl>
                        <div><dt>Head SHA</dt><dd class="mono">{c.head_sha}</dd></div>
                        <div><dt>PR created</dt><dd title={when(c.created_at)}>{ago(c.created_at) || 'unknown'}</dd></div>
                        <div><dt>PR updated</dt><dd title={when(c.updated_at)}>{ago(c.updated_at) || 'unknown'}</dd></div>
                        <div><dt>Discovered</dt><dd title={when(c.discovered_at)}>{ago(c.discovered_at) || 'unknown'}</dd></div>
                        <div><dt>Queue position</dt><dd>{c.queue_pos}</dd></div>
                      </dl>
                      <div class="review-history">
                        <h3>Reviews of this PR</h3>
                        {#if historyFor(c).length}
                          {#each historyFor(c) as r}
                            <p>
                              <span class="status {statusKind(r.verdict)}"><i></i>{statusLabel(r.verdict)}</span>
                              <span class="mono">{r.engine} · {r.head_sha?.slice(0, 8)}</span>
                              {#if r.head_sha === c.head_sha}<span class="tag">current head</span>{/if}
                              <time title={when(r.reviewed_at)}>{ago(r.reviewed_at)}</time>
                            </p>
                          {/each}
                        {:else}
                          <p class="muted">none recorded</p>
                        {/if}
                      </div>
                    </div>
                  {/if}
                </article>
              {/each}
            </div>
            {#if queue.length > 100}
              <button class="show" on:click={() => (queueShowAll = !queueShowAll)}>{queueShowAll ? 'Collapse' : `Show all ${queue.length}`}</button>
            {/if}
          {:else}
            <div class="empty">No queued PRs.</div>
          {/if}
        </section>

        <aside class="context">
          <section>
            <h2>Now</h2>
            <div class="now-grid">
              <div><strong>{queue.length}</strong><span>visible queue</span></div>
              <div><strong>{totalReviews}</strong><span>24h reviews</span></div>
              <div><strong>{approvedReviews}</strong><span>approved</span></div>
              <div><strong>{lastRun ? rel(lastRun.started_at) || 'just' : '–'}</strong><span>last run</span></div>
            </div>
            {#if lastRun}
              <p class="run-line"><span class="status {statusKind(lastRun.status)}"><i></i>{statusLabel(lastRun.status)}</span> {dur(lastRun.started_at, lastRun.finished_at)} on {lastRun.host}</p>
            {:else}
              <p class="muted">No runs yet.</p>
            {/if}
          </section>

          <section>
            <div class="section-head compact"><h2>Codex usage</h2>{#if usage?.plan}<span>plan {usage.plan}</span>{/if}</div>
            {#if !usageAvailable}
              <p class="muted">not available yet</p>
            {:else if usage?.error}
              <p class="muted">unavailable: {usage.error}</p>
            {:else}
              {#each [['Primary', usage?.primary], ['Secondary', usage?.secondary]] as item}
                {@const label = item[0] as string}
                {@const window = item[1] as UsageWindow | undefined}
                {#if window}
                  <div class:hot={window.used_percent >= 90} class="meter">
                    <div><span>{windowName(window, label)}</span><b>{Math.round(window.used_percent)}%</b></div>
                    <i style={`width:${Math.min(100, Math.max(0, window.used_percent))}%`}></i>
                    {#if window.resets_at}<small>resets {new Date(window.resets_at * 1000).toLocaleString()}</small>{/if}
                  </div>
                {/if}
              {/each}
            {/if}
          </section>

          <section>
            <div class="section-head compact"><h2>Activity</h2><span>{chartBars().total ? `${chartBars().total} total · peak ${chartBars().max}/h` : '24h'}</span></div>
            {#if chartBars().total}
              <div class="bars" style={`--peak:${chartBars().max}`}>
                {#each buckets as b}
                  <div title={`${new Date(b.hour).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })} - ${b.approved} approved, ${b.commented} commented, ${b.requested_changes} changes requested`}>
                    <i class="approved" style={`height:${Math.max(2, (b.approved / chartBars().max) * 100)}%`}></i>
                    <i class="commented" style={`height:${Math.max(2, (b.commented / chartBars().max) * 100)}%`}></i>
                    <i class="changes" style={`height:${Math.max(2, (b.requested_changes / chartBars().max) * 100)}%`}></i>
                  </div>
                {/each}
              </div>
              <div class="legend"><span><i class="approved"></i>Approved</span><span><i class="commented"></i>Commented</span><span><i class="changes"></i>Changes</span></div>
            {:else}
              <p class="muted">No reviews in the last 24h.</p>
            {/if}
          </section>

          <section>
            <h2>Recent runs</h2>
            {#if runs.length}
              <div class="mini-table">
                {#each runs as r}
                  <p><time title={when(r.started_at)}>{ago(r.started_at)}</time><span>{dur(r.started_at, r.finished_at)}</span><span class="status {statusKind(r.status)}"><i></i>{statusLabel(r.status)}</span><span>{r.host}</span></p>
                {/each}
              </div>
            {:else}
              <p class="muted">No runs yet.</p>
            {/if}
          </section>
        </aside>
      </div>

      <section class="review-strip">
        <div class="section-head">
          <div>
            <p class="eyebrow">Archive</p>
            <h2>Recent reviews</h2>
          </div>
          {#if reviews.length >= reviewLimit && reviewLimit < 500}<button class="show" on:click={() => { reviewLimit = Math.min(500, reviewLimit + 100); refreshOverview(); }}>Show more</button>{/if}
        </div>
        {#if reviews.length}
          <div class="review-list">
            {#each reviews as r}
              <p>
                <a href={prHref(r.repo, r.number)} target="_blank" rel="noopener">#{r.number}</a>
                <span>{r.repo}</span>
                <span class="status {statusKind(r.verdict)}"><i></i>{statusLabel(r.verdict)}</span>
                <span>{r.engine}</span>
                <span class="mono">{r.head_sha?.slice(0, 8)}</span>
                <time title={when(r.reviewed_at)}>{ago(r.reviewed_at)}</time>
              </p>
            {/each}
          </div>
        {:else}
          <div class="empty">No reviews yet.</div>
        {/if}
      </section>
    {:else if route === 'config'}
      <section class="page-head">
        <p class="eyebrow">Read-only</p>
        <h1>Configuration</h1>
        <p>Edit via the `repos` / `authors` CLIs and config.json.</p>
      </section>
      {#if configData}
        <div class="stack">
          <section class="surface">
            <div class="section-head"><h2>Watched repos</h2></div>
            {#if configData.repos?.length}
              <ul class="repo-list">
                {#each configData.repos as r}
                  <li><span>{r.name}</span>{#if r.allowed_authors_only}<span class="tag">allowed authors only</span>{/if}</li>
                {/each}
              </ul>
            {:else}
              <div class="empty">No repos. Add with: agent-code-review repos add owner/name</div>
            {/if}
          </section>
          <section class="surface">
            <div class="section-head"><h2>Settings</h2></div>
            <div class="settings">
              {#each [
                ['Reviewing as', configData.reviewing_as ? `@${configData.reviewing_as}` : 'unknown (gh not authenticated?)'],
                ['Review engine', configData.engine],
                ['Review loop (this daemon)', configData.review_running ? 'running' : configData.schedule.enabled ? 'off (config enabled, boot flag disabled)' : 'off'],
                ['Review interval', configData.schedule.interval],
                ['Max parallel reviews', configData.schedule.max_parallel],
                ['Discovery loop (this daemon)', configData.discovery_running ? 'running' : configData.discovery.enabled ? 'off (config enabled, boot flag disabled)' : 'off'],
                ['Discovery interval', configData.discovery.interval],
                ['New PR window (days)', configData.candidates.new_max_age_days],
                ['Refreshed PR window (days)', configData.candidates.refreshed_max_age_days],
              ] as row}
                <div><dt>{row[0]}</dt><dd>{row[1]}</dd></div>
              {/each}
            </div>
          </section>
          <section class="surface">
            <div class="section-head"><h2>Allowed authors</h2><span>whose PRs we may approve</span></div>
            {#if authors.length}
              <div class="table">
                <p><b>Repo</b><b>GitHub</b><b>Name</b><b>Slack</b></p>
                {#each authors as a}
                  <p><span>{a.repo === '*' ? 'all repos' : a.repo}</span><span>@{a.github_handle}</span><span>{a.name}</span><span>{a.slack_id}</span></p>
                {/each}
              </div>
            {:else}
              <div class="empty">No allowed authors. Every PR is comment-only.</div>
            {/if}
          </section>
        </div>
      {/if}
    {:else if route === 'prompt'}
      <section class="page-head">
        <p class="eyebrow">Read-only</p>
        <h1>Prompt assembly</h1>
        <p>Edit the review section of config.json.</p>
      </section>
      {#if promptData}
        <div class="stack">
          <section class="surface"><div class="section-head"><h2>Main prompt</h2></div><pre>{promptData.main_prompt || '(no main prompt configured)'}</pre></section>
          <section class="surface">
            <div class="section-head"><h2>Post-outcome instructions</h2><span>what the agent does after landing on each outcome</span></div>
            {#if outcomes.length}
              {#each outcomes as [k, v]}
                <div class="prompt-block"><h3>{k}</h3><pre>{v as string}</pre></div>
              {/each}
            {:else}
              <div class="empty">None configured.</div>
            {/if}
          </section>
          <section class="surface">
            <div class="section-head"><h2>Rules</h2><span>extra instructions when their condition matches</span></div>
            {#if promptData.rules?.length}
              {#each promptData.rules as r}
                <div class="prompt-block"><h3>{r.name}</h3><p class="muted">when: <code>{JSON.stringify(r.when || {})}</code></p><pre>{r.prompt}</pre></div>
              {/each}
            {:else}
              <div class="empty">No rules configured.</div>
            {/if}
          </section>
          <section class="surface">
            <div class="section-head"><h2>Assembled preview</h2><span>{promptData.note}</span></div>
            <div class="segmented">
              <label><input type="radio" bind:group={previewVariant} value="allowed_author" /> Allowed author</label>
              <label><input type="radio" bind:group={previewVariant} value="not_allowed_author" /> Author not allowed</label>
            </div>
            <pre>{activePreview}</pre>
          </section>
        </div>
      {/if}
    {:else if route === 'logs'}
      <section class="page-head">
        <p class="eyebrow">Daemon</p>
        <h1>Server logs</h1>
        <p>{logsAvailable ? `${logEntries.length} captured lines · in-memory tail` : 'log capture is not enabled in this process'}</p>
      </section>
      <section class="terminal" bind:this={logPane}>
        {#if logEntries.length}
          {#each logEntries as e}
            <p><time title={when(e.at)}>{new Date(e.at).toLocaleTimeString()}</time><span>{e.line}</span></p>
          {/each}
        {:else}
          <div class="empty">{logsAvailable ? 'No log lines captured yet.' : 'Log capture is not enabled in this process.'}</div>
        {/if}
      </section>
    {/if}
  </main>
</div>
