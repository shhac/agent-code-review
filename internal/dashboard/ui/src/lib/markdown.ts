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
// deeper indent than the current item opens a sublist inside it. It is
// drop-free: the outermost level starts at the minimum indent present, so every
// item is emitted even on irregular indentation — a shallower-than-its-sibling
// item flattens to the current level rather than vanishing (the previous
// equality-based grouping silently dropped such items).
function buildList(items: ListItem[]): string {
  let idx = 0;
  function level(minIndent: number): string {
    const ordered = items[idx].ordered;
    let html = ordered ? '<ol>' : '<ul>';
    while (idx < items.length && items[idx].indent >= minIndent) {
      const itemIndent = items[idx].indent;
      let li = '<li>' + items[idx].html;
      idx++;
      if (idx < items.length && items[idx].indent > itemIndent) {
        li += level(items[idx].indent);
      }
      li += '</li>';
      html += li;
    }
    html += ordered ? '</ol>' : '</ul>';
    return html;
  }
  return level(Math.min(...items.map((it) => it.indent)));
}

const LIST_RE = /^(\s*)([-*]|\d+\.)\s+(.*)$/;
const HEADING_RE = /^(#{1,6})\s+(.*)$/;

// takeWhile collects the run of lines from start for which pred holds, and
// returns it with the index of the first line that failed (or lines.length).
// The single cursor-advancing primitive the fence, list, and paragraph blocks
// share, so the loop reads as dispatch rather than three hand-rolled scans.
function takeWhile(lines: string[], start: number, pred: (s: string) => boolean): [string[], number] {
  let i = start;
  while (i < lines.length && pred(lines[i])) i++;
  return [lines.slice(start, i), i];
}

function listItem(line: string): ListItem {
  const m = line.match(LIST_RE)!;
  return { indent: m[1].length, ordered: /\d+\./.test(m[2]), html: inline(m[3]) };
}

export function mdToHtml(src: string): string {
  const lines = escapeHtml(src).split('\n');
  const out: string[] = [];
  let i = 0;
  const blank = (s: string) => s.trim() === '';
  const fence = (s: string) => s.trim().startsWith('```');
  // A line that begins a new block ends the current paragraph. One definition so
  // the paragraph terminator can't drift from the dispatch below.
  const isBlockStart = (s: string) => blank(s) || fence(s) || HEADING_RE.test(s) || LIST_RE.test(s);

  while (i < lines.length) {
    const line = lines[i];
    if (blank(line)) {
      i++;
      continue;
    }
    if (fence(line)) {
      const [code, end] = takeWhile(lines, i + 1, (l) => !fence(l));
      i = end + 1; // past the closing fence
      out.push('<pre><code>' + code.join('\n') + '</code></pre>');
      continue;
    }
    const h = line.match(HEADING_RE);
    if (h) {
      out.push(`<h${h[1].length}>${inline(h[2])}</h${h[1].length}>`);
      i++;
      continue;
    }
    if (LIST_RE.test(line)) {
      const [rows, end] = takeWhile(lines, i, (l) => LIST_RE.test(l));
      i = end;
      out.push(buildList(rows.map(listItem)));
      continue;
    }
    // Paragraph: consecutive plain lines; single newlines become <br>.
    const [para, end] = takeWhile(lines, i, (l) => !isBlockStart(l));
    i = end;
    out.push('<p>' + para.map(inline).join('<br>') + '</p>');
  }
  return out.join('\n');
}
