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
