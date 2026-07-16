// Minimal, dependency-free markdown -> HTML for the prompt previews. It renders
// only the constructs the assembled prompt uses (headings, bold, inline code,
// fenced code, ordered/unordered lists with one level of nesting, paragraphs).
//
// Security: the whole source is HTML-escaped FIRST, then tags are wrapped around
// the already-escaped text, so no markup in the content can reach the DOM. The
// output is safe to use with {@html} even though the content is the user's own.

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

// inline handles bold and inline code on already-escaped text. Code spans are
// pulled out first (behind a NUL sentinel that can't occur in escaped text) so
// ** and digits inside them stay literal.
function inline(s: string): string {
  const codes: string[] = [];
  let out = s.replace(/`([^`]+)`/g, (_m, c) => {
    codes.push(c);
    return `\x00${codes.length - 1}\x00`;
  });
  out = out.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  out = out.replace(/\x00(\d+)\x00/g, (_m, n) => `<code>${codes[Number(n)]}</code>`);
  return out;
}

type ListItem = { indent: number; ordered: boolean; html: string };

// buildList turns a flat run of list items into nested <ul>/<ol> by indent. A
// deeper indent than the current level opens a sublist inside the previous item.
function buildList(items: ListItem[]): string {
  let idx = 0;
  function level(indent: number): string {
    const ordered = items[idx].ordered;
    let html = ordered ? '<ol>' : '<ul>';
    while (idx < items.length && items[idx].indent === indent) {
      let li = '<li>' + items[idx].html;
      idx++;
      if (idx < items.length && items[idx].indent > indent) {
        li += level(items[idx].indent);
      }
      li += '</li>';
      html += li;
    }
    html += ordered ? '</ol>' : '</ul>';
    return html;
  }
  return level(items[0].indent);
}

const LIST_RE = /^(\s*)([-*]|\d+\.)\s+(.*)$/;
const HEADING_RE = /^(#{1,6})\s+(.*)$/;

export function mdToHtml(src: string): string {
  const lines = escapeHtml(src).split('\n');
  const out: string[] = [];
  let i = 0;
  const blank = (s: string) => s.trim() === '';
  const fence = (s: string) => s.trim().startsWith('```');

  while (i < lines.length) {
    const line = lines[i];
    if (blank(line)) {
      i++;
      continue;
    }
    if (fence(line)) {
      i++;
      const code: string[] = [];
      while (i < lines.length && !fence(lines[i])) {
        code.push(lines[i]);
        i++;
      }
      i++; // closing fence
      out.push('<pre><code>' + code.join('\n') + '</code></pre>');
      continue;
    }
    const h = line.match(HEADING_RE);
    if (h) {
      const n = h[1].length;
      out.push(`<h${n}>${inline(h[2])}</h${n}>`);
      i++;
      continue;
    }
    if (LIST_RE.test(line)) {
      const items: ListItem[] = [];
      while (i < lines.length && LIST_RE.test(lines[i])) {
        const m = lines[i].match(LIST_RE)!;
        items.push({ indent: m[1].length, ordered: /\d+\./.test(m[2]), html: inline(m[3]) });
        i++;
      }
      out.push(buildList(items));
      continue;
    }
    // Paragraph: consecutive plain lines; single newlines become <br>.
    const para: string[] = [];
    while (
      i < lines.length &&
      !blank(lines[i]) &&
      !fence(lines[i]) &&
      !HEADING_RE.test(lines[i]) &&
      !LIST_RE.test(lines[i])
    ) {
      para.push(inline(lines[i]));
      i++;
    }
    out.push('<p>' + para.join('<br>') + '</p>');
  }
  return out.join('\n');
}
