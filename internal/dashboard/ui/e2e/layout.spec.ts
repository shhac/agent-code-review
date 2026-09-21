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
    // By title, not by position: the fixture's newest row changes whenever a
    // case is added, and this test is about the row it then asserts on.
    const row = page.locator('.review-row', { hasText: 'generate entry QR codes' });
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

// The reward curve. It is the one place on the page where the meaning is the
// geometry, so the things that can break it are geometric: a line drawn
// outside the box it lives in, or landmarks that land on top of each other.
test.describe('score reward curve', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/config');
    await page.getByRole('tab', { name: 'Score tuning' }).click();
    await expect(page.locator('.reward-curve svg')).toBeVisible();
  });

  test('marks both landmarks without the labels colliding', async ({ page }) => {
    await expect(page.locator('.reward-curve .mark-label')).toHaveCount(2);
    expect(await overlappingPairs(page, '.reward-curve .mark-label')).toEqual([]);
  });

  test('draws the curve inside the panel it sits in', async ({ page }) => {
    const panel = await page.locator('.reward-curve').boundingBox();
    const rate = await page.locator('.reward-curve .rate').boundingBox();
    if (!panel || !rate) throw new Error('chart not laid out');
    // Not merely present: a polyline whose coordinates came out NaN renders as
    // a zero-size box, and an unscaled one overflows its figure.
    expect(rate.width).toBeGreaterThan(panel.width / 2);
    expect(rate.x).toBeGreaterThanOrEqual(panel.x - 1);
    expect(rate.x + rate.width).toBeLessThanOrEqual(panel.x + panel.width + 1);
  });
});

// The engine usage panel. Its one unbounded string is the paused reason, whose
// length the daemon decides ("weekly window has 9% remaining, floor is 15%")
// and the layout cannot. Stubbed rather than seeded: usage comes from polling
// the codex and claude CLIs, which the fixture daemon is started with
// --no-reviews precisely to avoid.
test.describe('engine usage panel', () => {
  const PAUSED = {
    available: true,
    engine: 'codex',
    review_paused: true,
    paused_reason: 'weekly window has 9% remaining, floor is 15%',
    fresh_tokens_total: 468_000_000,
    fresh_tokens_24h: 0,
    engines: [
      {
        engine: 'codex',
        active: true,
        available: true,
        paused: true,
        paused_reason: 'weekly window has 9% remaining, floor is 15%',
        usage: {
          plan: 'pro',
          primary: { window_mins: 10080, used_percent: 91, resets_at: 1790000000 },
        },
      },
      {
        engine: 'claude',
        active: false,
        available: true,
        usage: {
          plan: 'max',
          primary: { window_mins: 300, used_percent: 12 },
          secondary: { window_mins: 10080, used_percent: 40, resets_at: 1790400000 },
        },
      },
    ],
  };

  test.beforeEach(async ({ page }) => {
    await page.route('**/api/usage', (route) => route.fulfill({ json: PAUSED }));
    await page.goto('/');
    await expect(page.locator('.engine-usage').first()).toBeVisible();
  });

  // The bug: the reason renders in a <p class="status warn">, and .status is
  // nowrap so the inline badges in table rows keep their dot beside their
  // label. As a paragraph that nowrap made the sentence's min-content width
  // the aside's, the aside's auto track grew past the 370px column holding
  // it, and the whole PAGE gained a horizontal scrollbar -- pushing the Now
  // grid and the meters off the right edge of the window.
  test('the page does not scroll sideways', async ({ page }) => {
    const doc = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth,
    }));
    expect(doc.scrollWidth, 'the page scrolls horizontally').toBeLessThanOrEqual(doc.clientWidth + 1);
  });

  test('the panels stay inside their column', async ({ page }) => {
    const spills = await page.evaluate(() => {
      const column = document.querySelector('.context')!.getBoundingClientRect();
      return [...document.querySelectorAll('.context section')]
        .filter((e) => e.getBoundingClientRect().right > column.right + 1)
        .map((e) => `${e.querySelector('h2')?.textContent}: ${Math.round(e.getBoundingClientRect().width)}px in ${Math.round(column.width)}px`);
    });
    expect(spills).toEqual([]);
  });

  test('the paused reason is readable in full', async ({ page }) => {
    const line = page.locator('.engine-usage .status.warn');
    await expect(line).toContainText('floor is 15%');
    expect(await overflowing(page, '.engine-usage .status.warn')).toEqual([]);
  });

  // Each engine answers for itself: the panel-level line this replaced could
  // only ever speak for the active engine, so a held cohort on the OTHER
  // engine was invisible here.
  test('only the paused engine shows a pause note', async ({ page }) => {
    const blocks = page.locator('.engine-usage');
    await expect(blocks.filter({ hasText: 'codex' }).locator('.status.warn')).toHaveCount(1);
    await expect(blocks.filter({ hasText: 'claude' }).locator('.status.warn')).toHaveCount(0);
  });
});
