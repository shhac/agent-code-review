// Shared page helpers. esc() is the single escaping routine for everything
// interpolated into HTML — keep it here so a hardening fix lands on every
// page at once.
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) =>
  ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);

const when = (t) => t ? new Date(t).toLocaleString() : '';

// rel is the pure relative-time value ("5m", "3h"); ago wraps it for table
// cells with the full timestamp on hover. Go zero times serialize as year 1 —
// treat anything before 2000 as "not set".
const rel = (t) => {
  if (!t || new Date(t).getFullYear() < 2000) return '';
  const s = Math.max(0, (Date.now() - new Date(t).getTime()) / 1000);
  return s < 60 ? `${Math.floor(s)}s` :
    s < 3600 ? `${Math.floor(s / 60)}m` :
    s < 86400 ? `${Math.floor(s / 3600)}h` :
    `${Math.floor(s / 86400)}d`;
};
const ago = (t) => {
  const r = rel(t);
  return r && `<span class="when" title="${esc(when(t))}">${r} ago</span>`;
};

// Elapsed time between two timestamps ("42s", "8m", "1.5h").
const dur = (a, b) => {
  if (!a || !b) return '';
  const s = Math.max(0, (new Date(b) - new Date(a)) / 1000);
  return s < 90 ? `${Math.round(s)}s` : s < 5400 ? `${Math.round(s / 60)}m` : `${(s / 3600).toFixed(1)}h`;
};

// One vocabulary for candidate statuses, review verdicts, and run statuses.
// `live` states pulse their dot.
const ST_KIND = {
  queued: 'dim', reviewing: 'info live', reviewed: 'ok', skipped: 'warn', error: 'bad',
  APPROVED: 'ok', COMMENTED: 'info', REQUESTED_CHANGES: 'bad', SKIPPED: 'warn', ERROR: 'bad',
  running: 'info live', done: 'ok', failed: 'bad',
};
const badge = (s, label) => {
  const kind = ST_KIND[s] || 'dim';
  const cls = kind.split(' ').map((k) => `st-${k}`).join(' ');
  return `<span class="st ${cls}"><i class="dot"></i>${esc(label ?? String(s).replace(/_/g, ' '))}</span>`;
};

const fetchJSON = (url) => fetch(url).then((r) => r.json());

// One topbar for every page: brand, tabs, and either the live/stale feed
// indicator (default) or a static note (data-status on the header). Pages
// carry just `<header class="topbar" [data-status="…"]></header>` so a nav
// or brand change lands everywhere at once.
const TABS = [['/', 'Overview'], ['/config.html', 'Config'], ['/prompt.html', 'Prompt'], ['/logs.html', 'Logs']];
const BRAND_MARK = `<svg width="18" height="18" viewBox="0 0 18 18" fill="none" aria-hidden="true">
  <rect x="1" y="1" width="16" height="16" rx="3" stroke-width="1.5" class="mark-frame" />
  <path d="M5 6l3 3-3 3" stroke-width="1.5" class="mark-glyph" stroke-linecap="round" stroke-linejoin="round" />
  <path d="M9.5 12.5H13" stroke-width="1.5" class="mark-glyph" stroke-linecap="round" />
</svg>`;
const mountTopbar = () => {
  const bar = document.querySelector('header.topbar');
  if (!bar) return;
  const here = location.pathname === '/index.html' ? '/' : location.pathname;
  const tabs = TABS.map(([href, label]) =>
    `<a href="${href}"${href === here ? ' class="active"' : ''}>${label}</a>`).join('');
  const status = bar.dataset.status === undefined
    ? '<span class="topbar-status" id="feed"><i class="dot"></i><span id="feed-label">sync</span><span class="detail" id="feed-detail"></span></span>'
    : `<span class="topbar-status">${esc(bar.dataset.status)}</span>`;
  bar.innerHTML = `<span class="brand">${BRAND_MARK} agent-code-review</span><nav class="tabs">${tabs}</nav>${status}`;
};
mountTopbar();

// Topbar live/stale indicator, shared by every polling page.
const feed = (ok, detail = `· ${new Date().toLocaleTimeString()}`) => {
  const el = document.getElementById('feed');
  if (!el) return;
  el.classList.toggle('live', ok);
  el.classList.toggle('stale', !ok);
  document.getElementById('feed-label').textContent = ok ? 'live' : 'stale';
  document.getElementById('feed-detail').textContent = detail;
};
