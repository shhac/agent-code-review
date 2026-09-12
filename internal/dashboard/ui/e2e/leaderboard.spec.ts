import { expect, test } from '@playwright/test';

// The score column is a number sitting ON its own bar, which is two things
// that can collide. Both failures here were visible only once laid out: the
// digits touching the fill's edge, and a full-width fill with nothing behind
// it to compare against.
test.describe('the score column', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/leaderboard');
    await expect(page.locator('.board-row').first()).toBeVisible();
  });

  test('keeps the digits off the edge of their own cell', async ({ page }) => {
    const rows = await page.locator('.board-row .score').count();
    expect(rows).toBeGreaterThan(1);
    for (let i = 0; i < rows; i++) {
      const cell = await page.locator('.board-row .score').nth(i).boundingBox();
      const num = await page.locator('.board-row .score b').nth(i).boundingBox();
      if (!cell || !num) throw new Error('score cell not laid out');
      // Every row, not just the leader: the inset comes from padding on the
      // cell, so a row whose bar is short must be inset by the same amount.
      expect(num.x - cell.x).toBeGreaterThanOrEqual(8);
      expect(num.x + num.width).toBeLessThanOrEqual(cell.x + cell.width);
    }
  });

  test('shows the empty track behind a partial bar', async ({ page }) => {
    // The leader's bar is full by definition, so a one-author board proves
    // nothing. The second row is where a bar has to read as a proportion.
    const cell = await page.locator('.board-row .score').nth(1).boundingBox();
    const bar = await page.locator('.board-row .score .bar').nth(1).boundingBox();
    if (!cell || !bar) throw new Error('score bar not laid out');
    expect(bar.width).toBeGreaterThan(0);
    expect(bar.width).toBeLessThan(cell.width * 0.9);
    // The track is the cell's own background, so it spans the full width
    // whatever the fill does.
    await expect(page.locator('.board-row .score').nth(1)).not.toHaveCSS('background-color', 'rgba(0, 0, 0, 0)');
  });

  test('draws a negative total with no bar at all', async ({ page }) => {
    const row = page.locator('.board-row.negative').first();
    await expect(row).toBeVisible();
    const bar = await row.locator('.score .bar').boundingBox();
    // barWidth floors at zero, so there is nothing to fill: the colour of the
    // track and the digits is the only thing saying points were lost.
    expect(bar?.width ?? 0).toBeLessThan(1);
    await expect(row.locator('.score b')).toHaveCSS('color', 'rgb(234, 132, 120)');
  });
});

// Ranking by a different column is the whole point of the board being a table
// rather than a list. The fixture authors are deliberately different shapes:
// ada has volume, grace has judgment, and the two orderings disagree.
test.describe('ranking by a measure', () => {
  const names = (page: import('@playwright/test').Page) =>
    page.locator('.board-row .who strong').allTextContents();

  test.beforeEach(async ({ page }) => {
    await page.goto('/leaderboard');
    await expect(page.locator('.board-row').first()).toBeVisible();
  });

  test('opens on the total, where volume leads', async ({ page }) => {
    expect(await names(page)).toEqual(['ada', 'grace', 'octocat']);
    await expect(page.getByRole('button', { name: 'Total' })).toHaveAttribute('aria-pressed', 'true');
  });

  test('re-ranks on the typical review, where judgment leads', async ({ page }) => {
    await page.getByRole('button', { name: 'Median' }).click();
    await expect.poll(() => names(page)).toEqual(['grace', 'ada', 'octocat']);
    await expect(page.getByRole('button', { name: 'Median' })).toHaveAttribute('aria-pressed', 'true');
    await expect(page.getByRole('button', { name: 'Total' })).toHaveAttribute('aria-pressed', 'false');
  });

  test('moves the bar to whichever column it is ranked by', async ({ page }) => {
    // The bar lives in the ranked cell, so exactly one per row, and the leader
    // fills it whatever the measure.
    await expect(page.locator('.board-row .score .bar')).toHaveCount(3);
    await page.getByRole('button', { name: 'Mean' }).click();
    // Waited on the ORDER, not the bar count: the count is three before and
    // after, so polling it passes instantly against the old rows and the
    // geometry below gets measured on a board that has not re-ranked yet.
    // That raced on CI and not here, which is the usual shape of it.
    await expect.poll(() => names(page)).toEqual(['grace', 'ada', 'octocat']);

    const cell = await page.locator('.board-row .score').first().boundingBox();
    const bar = await page.locator('.board-row .score .bar').first().boundingBox();
    if (!cell || !bar) throw new Error('ranked cell not laid out');
    expect(bar.width).toBeGreaterThan(cell.width * 0.9);
  });

  // Removing code is the good outcome, so that board is ranked most-negative
  // first and a magnitude bar would draw the biggest adder like the biggest
  // remover. The column has none.
  test('draws no bar on the column whose best value is the smallest', async ({ page }) => {
    await page.getByRole('button', { name: 'Net lines' }).click();
    await expect.poll(() => names(page)).toEqual(['grace', 'ada', 'octocat']);
    await expect(page.locator('.board-row .score .bar')).toHaveCount(0);
  });

  // A single review is a mean of itself. The row still ranks; it is dimmed.
  test('dims a thin sample only where the measure is per-review', async ({ page }) => {
    await expect(page.locator('.score.thin')).toHaveCount(0);
    await page.getByRole('button', { name: 'Median' }).click();
    await expect(page.locator('.score.thin')).toHaveCount(1);
    await page.getByRole('button', { name: 'Reviews' }).click();
    await expect(page.locator('.score.thin')).toHaveCount(0);
  });
});
