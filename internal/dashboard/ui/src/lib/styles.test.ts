// Structural checks over the global stylesheet and the markup that depends on
// it. There is no DOM test runner here (no jsdom, no testing-library), so
// these read both as text — which is where the three bugs they guard actually
// lived.

import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const read = (rel: string) => readFileSync(fileURLToPath(new URL(rel, import.meta.url)), 'utf8');
const css = read('../app.css');

// stripComments and stripAtRuleBodies leave only top-level rules, so a
// declaration that lost its selector is visible rather than buried.
const bare = css.replace(/\/\*[\s\S]*?\*\//g, '');

describe('app.css is structurally sound', () => {
  it('has balanced braces', () => {
    const open = (bare.match(/\{/g) ?? []).length;
    const close = (bare.match(/\}/g) ?? []).length;
    expect(open, `unbalanced braces: ${open} { vs ${close} }`).toBe(close);
  });

  // The regression this exists for: removing a class from a shared selector
  // list line-by-line ("`.mini-table, .review-list { ... }`") deleted the
  // whole rule, leaving its declarations at top level with no selector. The
  // parser skips them silently, so the page just loses its layout.
  it('has no declaration block without a selector', () => {
    const orphans: string[] = [];
    let depth = 0;
    let selector = '';
    for (const raw of bare.split('\n')) {
      const line = raw.trim();
      // A SELECTOR line contains "{" even when it carries a pseudo-class
      // ("button:disabled, button:disabled:hover {"). A stranded DECLARATION
      // does not: it is "prop: value;" sitting at top level.
      const isDeclaration = !line.includes('{') && /^[a-z-]+\s*:\s*[^;]+;/i.test(line);
      if (depth === 0 && isDeclaration && !line.startsWith('--')) {
        orphans.push(line.slice(0, 70));
      }
      if (line.includes('{')) {
        if (depth === 0) selector = line.split('{')[0].trim();
        depth += (line.match(/\{/g) ?? []).length;
      }
      depth -= (line.match(/\}/g) ?? []).length;
      if (depth < 0) depth = 0;
    }
    expect(orphans, `declarations with no selector (a deleted rule header?): ${orphans.join(' | ')}`).toEqual([]);
    expect(selector.length).toBeGreaterThan(0);
  });
});

describe('disabled controls look disabled', () => {
  // Buttons are disabled in the add form until a URL is entered. Without a
  // :disabled rule the base button style still applies, so a click does
  // nothing and the only feedback is the absence of a result.
  it('styles button:disabled', () => {
    const rule = bare.match(/button:disabled[^{]*\{([^}]*)\}/);
    expect(rule, 'no button:disabled rule in app.css').toBeTruthy();
    expect(rule![1]).toMatch(/cursor:\s*not-allowed/);
    expect(rule![1]).toMatch(/opacity:/);
  });
});

describe('the history table lines up', () => {
  const history = read('../routes/History.svelte');

  // Header and rows are separate elements sharing one template, so a column
  // added to one and not the other silently shifts every row.
  const template = bare.match(/\.review-head,\s*\.review-row\s*\{[^}]*grid-template-columns:([^;]+);/);

  it('declares one shared template for the header and the rows', () => {
    expect(template, 'no shared .review-head/.review-row grid template').toBeTruthy();
  });

  it('has as many columns as the header renders cells', () => {
    const columns = template![1].trim().split(/\s+(?![^(]*\))/).length;
    const head = history.match(/<p class="review-head"[\s\S]*?<\/p>/)![0];
    const cells = (head.match(/<span/g) ?? []).length;
    expect(cells, `header renders ${cells} cells for ${columns} columns`).toBe(columns);
  });

  // The row's cell rules (nowrap, right-alignment) must not reach into the
  // detail panel below it, where a 40-character head SHA inherited nowrap and
  // overflowed across its neighbours. app.css documents the same trap for
  // `.authors > p`; a row's cells are its CHILDREN.
  it('scopes row cell rules to direct children', () => {
    const leaky = [...bare.matchAll(/^\s*\.review-(?:table|row)\s+\.(mono|num|chev)\b[^{]*\{/gm)]
      .map((m) => m[0].trim());
    expect(
      leaky,
      `descendant selectors reach into .review-detail: ${leaky.join(' | ')}`,
    ).toEqual([]);
    expect(bare).toMatch(/\.review-row\s*>\s*\.mono/);
  });

  // PrIdentity renders TWO elements. Dropped straight into the row it became
  // two grid items, pushing every later column one place left and wrapping
  // the chevron onto a second line. The wrapper is what keeps this table's
  // column count independent of that component's internals.
  it('wraps multi-element cells so each row cell is one grid item', () => {
    const row = history.match(/<p\s+class="review-row"[\s\S]*?<\/p>/)![0];
    expect(row, 'PrIdentity must be wrapped: it renders two elements').toMatch(
      /<span class="pr-cell"><PrIdentity/,
    );
    expect(bare).toMatch(/\.pr-cell\s*\{/);
  });
});
