// Turning an unknown rejection into something worth showing a person.
//
// Three call sites used `catch (e: any) { msg = e.message }`, which yields
// `undefined` for anything that is not an Error — a rejected non-Error, a
// thrown string — and renders as an empty error box: the one outcome worse
// than a bad message, because it looks like nothing happened. It also needs
// the `any` this project otherwise avoids.
export function errText(e: unknown): string {
  if (e instanceof Error && e.message) return e.message;
  const s = String(e);
  // String(undefined) and String({}) are not messages. Say plainly that the
  // failure had nothing to say rather than showing the reader "[object Object]".
  return s && s !== 'undefined' && s !== '[object Object]' ? s : 'request failed';
}
