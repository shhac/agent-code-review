import { describe, it, expect } from 'vitest';
import { mdToHtml } from './markdown';

describe('mdToHtml', () => {
  it('renders headings', () => {
    expect(mdToHtml('## If you COMMENTED')).toBe('<h2>If you COMMENTED</h2>');
  });

  it('renders an ordered list with a nested unordered sublist', () => {
    const src = ['1. First', '2. Second:', '   - a', '   - b', '3. Third'].join('\n');
    const html = mdToHtml(src);
    expect(html).toBe(
      '<ol><li>First</li><li>Second:<ul><li>a</li><li>b</li></ul></li><li>Third</li></ol>',
    );
  });

  it('never drops list items on irregular indentation', () => {
    // Decreasing indent: an item returns to column 0 after an indented one.
    const decreasing = mdToHtml(['- a', '   - b', '- c'].join('\n'));
    expect((decreasing.match(/<li>/g) || []).length).toBe(3);
    for (const item of ['a', 'b', 'c']) expect(decreasing).toContain(item);

    // Intermediate indent that matches no open level must still appear.
    const intermediate = mdToHtml(['- x', '      - y', '   - z'].join('\n'));
    expect((intermediate.match(/<li>/g) || []).length).toBe(3);
    expect(intermediate).toContain('z');

    // First item indented, later item at column 0.
    const shallowLater = mdToHtml(['   - p', '- q'].join('\n'));
    expect((shallowLater.match(/<li>/g) || []).length).toBe(2);
    expect(shallowLater).toContain('q');
  });

  it('renders bold and inline code without touching digits/asterisks inside code', () => {
    expect(mdToHtml('use **bold** and `a * b` and `x 5 y`')).toBe(
      '<p>use <strong>bold</strong> and <code>a * b</code> and <code>x 5 y</code></p>',
    );
  });

  it('does not treat a bare " 5 " as a code span', () => {
    expect(mdToHtml('step 5 done')).toBe('<p>step 5 done</p>');
  });

  it('renders fenced code verbatim (no inner formatting)', () => {
    const html = mdToHtml(['```', 'a **b** c', '```'].join('\n'));
    expect(html).toBe('<pre><code>a **b** c</code></pre>');
  });

  it('escapes HTML so content cannot inject markup', () => {
    expect(mdToHtml('<script>alert(1)</script>')).toBe('<p>&lt;script&gt;alert(1)&lt;/script&gt;</p>');
  });

  it('joins single newlines in a paragraph with <br>', () => {
    expect(mdToHtml('line one\nline two')).toBe('<p>line one<br>line two</p>');
  });

  it('separates blocks on blank lines', () => {
    expect(mdToHtml('para one\n\npara two')).toBe('<p>para one</p>\n<p>para two</p>');
  });
});

describe('thematic breaks', () => {
  it('renders --- as a rule, not literal text', () => {
    expect(mdToHtml('a\n\n---\n\nb')).toBe('<p>a</p>\n<hr>\n<p>b</p>');
  });

  it('accepts the other markers and spaced forms', () => {
    for (const rule of ['---', '***', '___', '- - -', '****', '   ---  ']) {
      expect(mdToHtml(rule)).toBe('<hr>');
    }
  });

  // `- - -` also satisfies the list pattern, so order of dispatch decides
  // whether it is a rule or three empty bullets.
  it('prefers a rule over a list for the spaced dash form', () => {
    expect(mdToHtml('- - -')).not.toContain('<li>');
  });

  it('leaves genuine list items and text alone', () => {
    expect(mdToHtml('- a')).toBe('<ul><li>a</li></ul>');
    expect(mdToHtml('--')).toBe('<p>--</p>');
    expect(mdToHtml('a -- b')).toBe('<p>a -- b</p>');
  });

  it('ends the paragraph above it', () => {
    expect(mdToHtml('text\n---\nmore')).toBe('<p>text</p>\n<hr>\n<p>more</p>');
  });
});

describe('links', () => {
  it('renders an inert span carrying the target, never an href', () => {
    const html = mdToHtml('See [the guide](https://example.com/g).');
    expect(html).toContain('data-url="https://example.com/g"');
    expect(html).toContain('>the guide</span>');
    // The whole point: nothing in the output can navigate on a stray click.
    expect(html).not.toContain('href');
    expect(html).not.toContain('<a ');
  });

  it('refuses schemes that are not http(s) or mailto, leaving the source visible', () => {
    for (const bad of [
      '[x](javascript:alert(1))',
      '[x](data:text/html;base64,PHNjcmlwdD4=)',
      '[x](vbscript:evil)',
      '[x](/relative/path)',
      '[x](JavaScript:alert(1))',
    ]) {
      const html = mdToHtml(bad);
      expect(html).not.toContain('mdlink');
      expect(html).not.toContain('data-url');
    }
  });

  it('allows mailto', () => {
    expect(mdToHtml('[mail](mailto:a@b.com)')).toContain('data-url="mailto:a@b.com"');
  });

  it('cannot break out of the data-url attribute', () => {
    const html = mdToHtml('[x](https://e.com/"onmouseover="alert(1))');
    // The quote is escaped upstream, so it cannot terminate the attribute.
    expect(html).not.toContain('onmouseover="alert');
    expect(html).not.toMatch(/data-url="[^"]*"[a-z]/);
  });

  it('does not let emphasis rewrite the target', () => {
    // A URL with underscores or asterisks must survive verbatim, or the link
    // silently points somewhere else.
    expect(mdToHtml('[a](https://e.com/_x_/y)')).toContain('data-url="https://e.com/_x_/y"');
    expect(mdToHtml('[a](https://e.com/a*b*c)')).toContain('data-url="https://e.com/a*b*c"');
  });

  it('formats the link text but not the target', () => {
    expect(mdToHtml('[**bold** link](https://e.com)')).toContain('<strong>bold</strong> link</span>');
  });
});

describe('emphasis', () => {
  it('renders italic with either marker', () => {
    expect(mdToHtml('an *emphasised* word')).toBe('<p>an <em>emphasised</em> word</p>');
    expect(mdToHtml('an _emphasised_ word')).toBe('<p>an <em>emphasised</em> word</p>');
  });

  it('leaves identifiers with underscores alone', () => {
    // This codebase is full of them; emphasising mid-word would mangle most
    // prompts that mention a config key.
    for (const src of ['max_budget_usd', 'a head_sha value', 'work_dir and file_name', 'a_b_c_d']) {
      expect(mdToHtml(src)).toBe(`<p>${src}</p>`);
    }
  });

  it('resolves *** as bold and italic together', () => {
    expect(mdToHtml('***very***')).toBe('<p><em><strong>very</strong></em></p>');
  });

  it('still does not format inside code spans', () => {
    expect(mdToHtml('`a *b* c_d_e`')).toBe('<p><code>a *b* c_d_e</code></p>');
  });

  it('leaves unpaired markers as text', () => {
    expect(mdToHtml('a * b')).toBe('<p>a * b</p>');
    expect(mdToHtml('a ** b')).toBe('<p>a ** b</p>');
  });
});

describe('coverage gaps that were previously unpinned', () => {
  it('escapes ampersands and quotes, not just angle brackets', () => {
    expect(mdToHtml('a & b "c"')).toBe('<p>a &amp; b &quot;c&quot;</p>');
  });

  it('supports every heading level and rejects a seventh', () => {
    expect(mdToHtml('# One')).toBe('<h1>One</h1>');
    expect(mdToHtml('###### Six')).toBe('<h6>Six</h6>');
    expect(mdToHtml('####### Seven')).toBe('<p>####### Seven</p>');
    expect(mdToHtml('#NoSpace')).toBe('<p>#NoSpace</p>');
  });

  it('renders an unclosed fence rather than dropping it', () => {
    expect(mdToHtml('```\nstill shown')).toBe('<pre><code>still shown</code></pre>');
  });

  it('returns nothing for empty or whitespace-only input', () => {
    expect(mdToHtml('')).toBe('');
    expect(mdToHtml('   \n\n  ')).toBe('');
  });

  it('applies inline formatting inside headings and list items', () => {
    expect(mdToHtml('## a **b** `c`')).toBe('<h2>a <strong>b</strong> <code>c</code></h2>');
    expect(mdToHtml('- a **b** `c`')).toBe('<ul><li>a <strong>b</strong> <code>c</code></li></ul>');
  });
});
