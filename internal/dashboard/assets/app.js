// Shared page helpers. esc() is the single escaping routine for everything
// interpolated into HTML — keep it here so a hardening fix lands on every
// page at once.
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) =>
  ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);

const when = (t) => t ? new Date(t).toLocaleString() : '';

const fetchJSON = (url) => fetch(url).then((r) => r.json());
