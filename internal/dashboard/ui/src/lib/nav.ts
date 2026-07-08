// The single SPA navigation entry point, used by the rail links and by
// in-route links alike: push the path, then trip App's popstate listener —
// the one place the current route is recomputed.

export function navigate(path: string) {
  history.pushState({}, '', path);
  window.dispatchEvent(new PopStateEvent('popstate'));
}
