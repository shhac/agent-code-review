import { expect, test, type Locator, type Page } from '@playwright/test';

// Layout invariants, asserted as rules rather than compared against a
// screenshot baseline. A pixel diff would catch these too, but it re-baselines
// on every intentional change and reports a pink smear; these name the two
// cells that collided.

/** Bounding boxes of an element's direct children, left to right. */
async function childBoxes(el: Locator) {
  return el.evaluate((node) =>
    [...node.children].map((c) => {
      const r = c.getBoundingClientRect();
      return { left: Math.round(r.left), right: Math.round(r.right), top: Math.round(r.top), bottom: Math.round(r.bottom) };
    }),
  );
}

/** Every pair of elements that visually intersects, named by its text. */
async function overlappingPairs(page: Page, selector: string) {
  return page.evaluate((sel) => {
    const els = [...document.querySelectorAll(sel)];
    const hits: string[] = [];
    for (let i = 0; i < els.length; i++) {
      for (let j = i + 1; j < els.length; j++) {
        const a = els[i].getBoundingClientRect();
        const b = els[j].getBoundingClientRect();
        if (a.left < b.right - 1 && b.left < a.right - 1 && a.top < b.bottom - 1 && b.top < a.bottom - 1) {
          hits.push(`"${els[i].textContent?.trim().slice(0, 30)}" over "${els[j].textContent?.trim().slice(0, 30)}"`);
        }
      }
    }
    return hits;
  }, selector);
}

/** Elements whose text is wider than the box holding it, named with both widths. */
async function overflowing(page: Page, selector: string) {
  return page.evaluate(
    (sel) =>
      [...document.querySelectorAll(sel)]
        .filter((e) => e.scrollWidth > e.clientWidth + 1)
        .map((e) => `${e.textContent?.trim().slice(0, 44)} (${e.scrollWidth}px in ${e.clientWidth}px)`),
    selector,
  );
}

test.describe('history table', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/history');
    await expect(page.locator('.review-row').first()).toBeVisible();
  });

  // Header and rows are separate elements sharing one grid template. A cell
  // added to one and not the other shifts every column silently — which is
  // what happened when PrIdentity's two elements were dropped in unwrapped.
  test('every row lines up with the header', async ({ page }) => {
    const head = await childBoxes(page.locator('.review-head'));
    const rows = page.locator('.review-row');
    for (let i = 0; i < (await rows.count()); i++) {
      const cells = await childBoxes(rows.nth(i));
      expect(cells.length, `row ${i} has ${cells.length} cells for ${head.length} columns`).toBe(head.length);
      cells.forEach((c, col) => {
        expect(Math.abs(c.left - head[col].left), `row ${i} column ${col} is offset from the header`).toBeLessThanOrEqual(2);
      });
    }
  });

  // The bug that shipped: .review-table .mono was a descendant selector, so
  // the 40-character head SHA in the expanded panel inherited nowrap and its
  // TEXT ran across the neighbouring cells.
  //
  // Note the boxes never intersected — the grid kept those in place and only
  // the painted text escaped — so a box-intersection check passes on the
  // broken build. Content overflow is the property that actually breaks.
  test('no value overflows its cell', async ({ page }) => {
    for (const row of await page.locator('.review-row').all()) await row.click();
    await expect(page.locator('.review-detail').first()).toBeVisible();
    expect(await overflowing(page, '.review-detail dd, .review-detail dt')).toEqual([]);
  });

  test('no two cells occupy the same space', async ({ page }) => {
    for (const row of await page.locator('.review-row').all()) await row.click();
    expect(await overlappingPairs(page, '.review-detail dl div')).toEqual([]);
  });

  test('nothing spills outside the panel', async ({ page }) => {
    for (const row of await page.locator('.review-row').all()) await row.click();
    const spills = await page.evaluate(() => {
      const panel = document.querySelector('.review-table')!.getBoundingClientRect();
      return [...document.querySelectorAll('.review-table *')]
        .filter((e) => e.getBoundingClientRect().right > panel.right + 1)
        .map((e) => `${e.tagName}: ${e.textContent?.trim().slice(0, 40)}`);
    });
    expect(spills).toEqual([]);
  });

  test('the row expands and collapses', async ({ page }) => {
    const row = page.locator('.review-row').first();
    await expect(page.locator('.review-detail')).toHaveCount(0);
    await row.click();
    await expect(row).toHaveAttribute('aria-expanded', 'true');
    await expect(page.locator('.review-detail').first()).toContainText('c6bce3be1b32293bde57f0982026309b78e61403');
    await row.click();
    await expect(page.locator('.review-detail')).toHaveCount(0);
  });

  test('values are not jammed against their neighbours', async ({ page }) => {
    // Svelte collapses whitespace between an expression and an adjacent
    // element, which rendered "$0.8011estimated" and "gpt-5.6-terra· medium".
    await page.locator('.review-row').first().click();
    const model = page.locator('.review-detail dd.mono', { hasText: 'gpt-' }).first();
    await expect(model).toContainText(/gpt-[\w.-]+ · \w+/);
  });
});

test.describe('add form', () => {
  // Disabled buttons had no disabled appearance: clicking simply did nothing
  // and the only feedback was the absence of a result.
  test('buttons show they are disabled until a PR is entered', async ({ page }) => {
    await page.goto('/');
    const buttons = page.locator('.add button');
    await expect(buttons).toHaveCount(2);

    for (const b of await buttons.all()) {
      await expect(b).toBeDisabled();
      await expect(b).toHaveCSS('cursor', 'not-allowed');
      expect(Number(await b.evaluate((e) => getComputedStyle(e).opacity))).toBeLessThan(0.75);
    }

    await page.locator('.add input').fill('acme/widgets/pull/9');
    for (const b of await buttons.all()) {
      await expect(b).toBeEnabled();
      await expect(b).toHaveCSS('cursor', 'pointer');
    }
  });

  test('the buttons sit beside the input, not below it', async ({ page }) => {
    await page.goto('/');
    const boxes = await childBoxes(page.locator('.add'));
    const [input, queue, steer] = boxes;
    expect(Math.abs(queue.top - input.top), 'Queue wrapped onto another line').toBeLessThanOrEqual(4);
    expect(Math.abs(steer.top - input.top), 'Steer wrapped onto another line').toBeLessThanOrEqual(4);
    expect(input.right).toBeLessThanOrEqual(queue.left + 1);
    expect(queue.right).toBeLessThanOrEqual(steer.left + 1);
  });
});

test.describe('queue tickets', () => {
  // The expanded ticket renders the same 40-character head SHA in a dd.mono as
  // the history panel does, so it is exposed to the same stray nowrap. The two
  // panels are on different routes, so one test cannot cover both.
  test('no value overflows its cell', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.ticket-main').first()).toBeVisible();
    for (const t of await page.locator('.ticket-main').all()) await t.click();
    await expect(page.locator('.ticket-detail').first()).toBeVisible();
    expect(await overflowing(page, '.ticket-detail dd, .ticket-detail dt')).toEqual([]);
  });

  test('no two cells occupy the same space', async ({ page }) => {
    await page.goto('/');
    for (const t of await page.locator('.ticket-main').all()) await t.click();
    expect(await overlappingPairs(page, '.ticket-detail dl div')).toEqual([]);
  });
});
