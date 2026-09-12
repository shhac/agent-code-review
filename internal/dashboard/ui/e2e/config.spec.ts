import { expect, test } from '@playwright/test';

// The Config page's two scoring panels. Both exist to explain a number, so
// what is worth asserting is that they agree with the daemon and with each
// other rather than that they render.
test.describe('score tiers', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/config');
    await expect(page.locator('.tier-table tbody tr').first()).toBeVisible();
  });

  test('states every tier the chart draws', async ({ page }) => {
    const names = await page.locator('.tier-table tbody tr td:first-child').allTextContents();
    const bands = await page.locator('.curve .band').allTextContents();
    expect(names).toEqual(bands.map((b) => b.trim()));
  });

  test('quotes the open-ended tier without an upper bound', async ({ page }) => {
    const last = page.locator('.tier-table tbody tr').last();
    await expect(last.locator('td').nth(1)).toContainText('+');
  });
});

test.describe('score calculator', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/config');
    await expect(page.locator('.calc-total')).toBeVisible();
  });

  test('prices a PR the way the scorer does', async ({ page }) => {
    // 200 added + 100 removed at the default 0.5 weight is 250 churn, which is
    // exactly the medium anchor, so the rate is a clean 1x and the total is
    // the base rate times the churn. Picked because it is the one worked
    // example that reads the same under either curve.
    await expect(page.locator('.calc-total')).toContainText('+500');
    await expect(page.locator('.calc-why')).toContainText('250');
    await expect(page.locator('.calc-why')).toContainText('medium');
  });

  test('turns negative when the verdict costs points', async ({ page }) => {
    await page.locator('.calc select').last().selectOption('REQUESTED_CHANGES');
    await expect(page.locator('.calc-total')).toContainText('-125');
    await expect(page.locator('.calc-total.bad')).toBeVisible();
  });

  test('shows the decay once there is more than one round', async ({ page }) => {
    // The rounds list is the whole reason the calculator models a sequence:
    // approving at round three is worth less than approving at round one, and
    // a single-figure answer would hide it.
    await expect(page.locator('.calc-rounds li')).toHaveCount(0);
    await page.locator('.calc select').first().selectOption('3');
    await expect(page.locator('.calc-rounds li')).toHaveCount(3);

    const scores = await page.locator('.calc-rounds li b').allTextContents();
    const finalRound = Number(scores[2].replace('+', ''));
    expect(finalRound).toBeLessThan(500);
    expect(finalRound).toBeGreaterThan(0);
  });
});
