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
